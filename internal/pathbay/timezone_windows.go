package pathbay

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"golang.org/x/sys/windows/registry"
	"os"
)

//go:embed windows_zones.json
var windowsZones []byte

func localTimezone() (string, []byte, error) {
	if name := os.Getenv("TZ"); name != "" {
		data, err := zoneBytes(name)
		return name, data, err
	}
	key, err := registry.OpenKey(registry.LOCAL_MACHINE, `SYSTEM\CurrentControlSet\Control\TimeZoneInformation`, registry.QUERY_VALUE)
	if err != nil {
		return "", nil, err
	}
	defer key.Close()
	custom, _, e := key.GetIntegerValue("DynamicDaylightTimeDisabled")
	if e == nil && custom != 0 {
		return "", nil, fmt.Errorf("custom Windows daylight-saving rules are unsupported")
	}
	name, _, err := key.GetStringValue("TimeZoneKeyName")
	if err != nil {
		return "", nil, err
	}
	var mapping map[string]string
	if err = json.Unmarshal(windowsZones, &mapping); err != nil {
		return "", nil, err
	}
	iana := mapping[name]
	if iana == "" {
		return "", nil, fmt.Errorf("unsupported Windows timezone %s", name)
	}
	data, err := zoneBytes(iana)
	return iana, data, err
}
