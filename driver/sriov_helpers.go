package driver

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/vishvananda/netlink"
)

const (
	netSysDir        = "/sys/class/net"
	netDevPrefix     = "device"
	netdevDriverDir  = "device/driver"
	netdevUnbindFile = "unbind"
	netdevBindFile   = "bind"

	netDevCurrentVFCountFile = "sriov_numvfs"
	netDevVFDevicePrefix     = "virtfn"
)

func netDevDeviceDir(netDevName string) string {
	devDirName := netSysDir + "/" + netDevName + "/" + netDevPrefix
	return devDirName
}

func netdevGetEnabledVFCount(name string) (int, error) {
	devDirName := netDevDeviceDir(name)

	file := devDirName + "/" + netDevCurrentVFCountFile
	maxDevFile, err := os.OpenFile(file, os.O_RDONLY, 0444)
	if err != nil {
		return 0, err
	}
	defer maxDevFile.Close()

	b, err := io.ReadAll(maxDevFile)
	if err != nil {
		return 0, err
	}
	str := string(b)
	str = strings.TrimSpace(str)
	curVfs, err := strconv.Atoi(str)
	if err != nil {
		return 0, err
	} else {
		fmt.Println("cur_vfs = ", curVfs)
		return curVfs, nil
	}
}

func SetVFVlan(parentNetdev string, vfDir string, vlan int) error {
	vfIndexStr := strings.TrimPrefix(vfDir, "virtfn")
	vfIndex, _ := strconv.Atoi(vfIndexStr)

	parentHandle, err1 := netlink.LinkByName(parentNetdev)
	if err1 != nil {
		return err1
	}

	err2 := netlink.LinkSetVfVlan(parentHandle, vfIndex, vlan)
	return err2
}

func SetVFPrivileged(parentNetdev string, vfDir string, privileged bool) error {
	var spoofChk bool
	var trusted bool

	vfIndexStr := strings.TrimPrefix(vfDir, "virtfn")
	vfIndex, _ := strconv.Atoi(vfIndexStr)

	if privileged {
		spoofChk = false
		trusted = true
	} else {
		spoofChk = true
		trusted = false
	}

	parentHandle, err := netlink.LinkByName(parentNetdev)
	if err != nil {
		return err
	}
	/* do not check for error status as older kernels doesn't
	 * have support for it.
	 */
	netlink.LinkSetVfTrust(parentHandle, vfIndex, trusted)
	netlink.LinkSetVfSpoofchk(parentHandle, vfIndex, spoofChk)
	return err
}

func IsSRIOVSupported(netdevName string) bool {
	maxvfs, err := netdevGetEnabledVFCount(netdevName)
	if maxvfs == 0 || err != nil {
		return false
	} else {
		return true
	}
}
