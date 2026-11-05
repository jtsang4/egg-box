//go:build windows

package winplat

import (
	"errors"
	"fmt"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// netClass is the device setup class of network adapters.
var netClass = windows.GUID{Data1: 0x4d36e972, Data2: 0xe325, Data3: 0x11ce, Data4: [8]byte{0xbf, 0xc1, 0x08, 0x00, 0x2b, 0xe1, 0x03, 0x18}}

var errAdapterNotFound = errors.New("the adapter is not there")

// removeAdapter removes the virtual network device that has this adapter GUID,
// the way the Device Manager's "Uninstall device" does. It removes nothing but
// a Wintun device: anything else with that GUID is refused.
//
// It is the last resort of cleanup. The core removes its own adapter when it
// stops and the system removes it when the process that made it ends; this is
// for the case where neither happened.
func removeAdapter(guid string) error {
	set, err := windows.SetupDiGetClassDevsEx(&netClass, "", 0, windows.DIGCF_PRESENT, 0, "")
	if err != nil {
		return fmt.Errorf("list the network devices: %w", err)
	}
	defer set.Close()

	for i := 0; ; i++ {
		dev, err := set.EnumDeviceInfo(i)
		if err != nil {
			if errors.Is(err, windows.ERROR_NO_MORE_ITEMS) {
				return errAdapterNotFound
			}
			return fmt.Errorf("list the network devices: %w", err)
		}
		id, err := netCfgInstanceID(set, dev)
		if err != nil || !SameGUID(id, guid) {
			continue
		}
		desc, _ := set.DeviceRegistryProperty(dev, windows.SPDRP_DEVICEDESC)
		if s, _ := desc.(string); !IsWintun(strings.TrimSpace(s)) {
			return fmt.Errorf("refusing to remove a device that is not a Wintun adapter (%q)", s)
		}
		params := windows.RemoveDeviceParams{
			ClassInstallHeader: *windows.MakeClassInstallHeader(windows.DIF_REMOVE),
			Scope:              windows.DI_REMOVEDEVICE_GLOBAL,
		}
		if err := set.SetClassInstallParams(dev, &params.ClassInstallHeader, uint32(unsafe.Sizeof(params))); err != nil {
			return fmt.Errorf("prepare removing the adapter: %w", err)
		}
		if err := set.CallClassInstaller(windows.DIF_REMOVE, dev); err != nil {
			return fmt.Errorf("remove the adapter: %w", err)
		}
		return nil
	}
}

// netCfgInstanceID reads the adapter GUID the network stack knows a device by.
func netCfgInstanceID(set windows.DevInfo, dev *windows.DevInfoData) (string, error) {
	h, err := set.OpenDevRegKey(dev, windows.DICS_FLAG_GLOBAL, 0, windows.DIREG_DRV, windows.KEY_QUERY_VALUE)
	if err != nil {
		return "", err
	}
	key := registry.Key(h)
	defer key.Close()
	s, _, err := key.GetStringValue("NetCfgInstanceId")
	return s, err
}
