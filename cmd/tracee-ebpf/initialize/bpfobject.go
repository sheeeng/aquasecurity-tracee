package initialize

import (
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"

	"github.com/aquasecurity/libbpfgo/helpers"
	embed "github.com/aquasecurity/tracee"
	"github.com/aquasecurity/tracee/cmd/tracee-ebpf/internal/debug"
	tracee "github.com/aquasecurity/tracee/pkg/ebpf"
)

func BpfObject(config *tracee.Config, kConfig *helpers.KernelConfig, OSInfo *helpers.OSInfo, installPath string, version string) error {
	var d = struct {
		btfenv     bool
		bpfenv     bool
		btfvmlinux bool
	}{
		btfenv:     false,
		bpfenv:     false,
		btfvmlinux: helpers.OSBTFEnabled(),
	}

	debug := config.Debug

	bpfFilePath, err := checkEnvPath("TRACEE_BPF_FILE")
	if bpfFilePath != "" {
		d.bpfenv = true
	} else if bpfFilePath == "" && err != nil {
		return err
	}
	btfFilePath, err := checkEnvPath("TRACEE_BTF_FILE")
	if btfFilePath != "" {
		d.btfenv = true
	} else if btfFilePath == "" && err != nil {
		return err
	}
	if debug {
		fmt.Printf("BTF: bpfenv = %v, btfenv = %v, vmlinux = %v\n", d.bpfenv, d.btfenv, d.btfvmlinux)
	}

	var tVersion, kVersion string
	var bpfBytes []byte
	var unpackBTFFile string

	// Decision ordering:

	// (1) BPF file given & BTF (vmlinux or env) exists: always load BPF as CO-RE
	// (2) BPF file given & if no BTF exists: it is a non CO-RE BPF

	if d.bpfenv {
		if debug {
			fmt.Printf("BPF: using BPF object from environment: %v\n", bpfFilePath)
		}
		if d.btfvmlinux || d.btfenv { // (1)
			if d.btfenv {
				if debug {
					fmt.Printf("BTF: using BTF file from environment: %v\n", btfFilePath)
				}
				config.BTFObjPath = btfFilePath
			}
		} // else {} (2)
		if bpfBytes, err = ioutil.ReadFile(bpfFilePath); err != nil {
			return err
		}

		goto out
	}

	// (3) no BPF file given & BTF (vmlinux or env) exists: load embedded BPF as CO-RE

	if d.btfvmlinux || d.btfenv { // (3)
		if debug {
			fmt.Println("BPF: using embedded BPF object")
		}
		if d.btfenv {
			if debug {
				fmt.Printf("BTF: using BTF file from environment: %v\n", btfFilePath)
			}
			config.BTFObjPath = btfFilePath
		}
		bpfFilePath = "embedded-core"
		bpfBytes, err = unpackCOREBinary()
		if err != nil {
			return fmt.Errorf("could not unpack embedded CO-RE eBPF object: %v", err)
		}

		goto out
	}

	// (4) no BPF file given & no BTF available: check embedded BTF files

	unpackBTFFile = filepath.Join(installPath, "/tracee.btf")
	err = unpackBTFHub(unpackBTFFile, OSInfo)

	if err == nil {
		if debug {
			fmt.Printf("BTF: using BTF file from embedded btfhub: %v\n", unpackBTFFile)
		}
		config.BTFObjPath = unpackBTFFile
		bpfFilePath = "embedded-core"
		bpfBytes, err = unpackCOREBinary()
		if err != nil {
			return fmt.Errorf("could not unpack embedded CO-RE eBPF object: %v", err)
		}

		goto out
	}

	// (5) no BPF file given & no BTF available & no embedded BTF: non CO-RE BPF

	tVersion = strings.ReplaceAll(version, "\"", "")
	tVersion = strings.ReplaceAll(tVersion, ".", "_")
	kVersion = OSInfo.GetOSReleaseFieldValue(helpers.OS_KERNEL_RELEASE)
	kVersion = strings.ReplaceAll(kVersion, ".", "_")

	bpfFilePath = fmt.Sprintf("%s/tracee.bpf.%s.%s.o", installPath, kVersion, tVersion)
	if debug {
		fmt.Printf("BPF: no BTF file was found or provided\n")
		fmt.Printf("BPF: trying non CO-RE eBPF at %s\n", bpfFilePath)
	}
	if bpfBytes, err = ioutil.ReadFile(bpfFilePath); err != nil {
		// tell entrypoint that eBPF non CO-RE obj compilation is needed
		fmt.Printf("BPF: %v\n", err)
		fmt.Printf("BPF: ATTENTION:\n")
		fmt.Printf("BPF: It seems tracee-ebpf can't load CO-RE eBPF obj and could not find\n")
		fmt.Printf("BPF: the non CO-RE object in %s. You may build a non CO-RE eBPF\n", installPath)
		fmt.Printf("BPF: obj by using the source tree and executing \"make install-bpf-nocore\".\n")
		os.Exit(2)
	}

out:
	config.KernelConfig = kConfig
	config.BPFObjPath = bpfFilePath
	config.BPFObjBytes = bpfBytes

	return nil
}

func checkEnvPath(env string) (string, error) {
	filePath, _ := os.LookupEnv(env)
	if filePath != "" {
		_, err := os.Stat(filePath)
		if err != nil {
			return "", fmt.Errorf("could not open %s %s", env, filePath)
		}
		return filePath, nil
	}
	return "", nil
}

func unpackCOREBinary() ([]byte, error) {
	b, err := embed.BPFBundleInjected.ReadFile("dist/tracee.bpf.core.o")
	if err != nil {
		return nil, err
	}

	if debug.Enabled() {
		fmt.Println("unpacked CO:RE bpf object file into memory")
	}

	return b, nil
}

// unpackBTFHub unpacks tailored, to the compiled eBPF object, BTF files for kernel supported by BTFHub
func unpackBTFHub(outFilePath string, OSInfo *helpers.OSInfo) error {
	var btfFilePath string

	osId := OSInfo.GetOSReleaseFieldValue(helpers.OS_ID)
	versionId := strings.Replace(OSInfo.GetOSReleaseFieldValue(helpers.OS_VERSION_ID), "\"", "", -1)
	kernelRelease := OSInfo.GetOSReleaseFieldValue(helpers.OS_KERNEL_RELEASE)
	arch := OSInfo.GetOSReleaseFieldValue(helpers.OS_ARCH)

	if err := os.MkdirAll(filepath.Dir(outFilePath), 0755); err != nil {
		return fmt.Errorf("could not create temp dir: %s", err.Error())
	}

	btfFilePath = fmt.Sprintf("dist/btfhub/%s/%s/%s/%s.btf", osId, versionId, arch, kernelRelease)
	btfFile, err := embed.BPFBundleInjected.Open(btfFilePath)
	if err != nil {
		return fmt.Errorf("error opening embedded btfhub file: %s", err.Error())
	}
	defer btfFile.Close()

	outFile, err := os.Create(outFilePath)
	if err != nil {
		return fmt.Errorf("could not create btf file: %s", err.Error())
	}
	defer outFile.Close()

	if _, err := io.Copy(outFile, btfFile); err != nil {
		return fmt.Errorf("error copying embedded btfhub file: %s", err.Error())

	}

	return nil
}
