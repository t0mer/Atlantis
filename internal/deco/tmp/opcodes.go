package tmp

import "fmt"

// Opcode is an AppV2 operation code (libtpnetwork/mesh/global/a0.java).
type Opcode uint16

// Session opcodes.
const (
	OpTokenAlloc    Opcode = 0x0001
	OpTokenVerify   Opcode = 0x0002
	OpTokenFree     Opcode = 0x0003
	OpCompNegotiate Opcode = 0x4001
)

// Write opcodes. Each is allowed only through its dedicated, validated
// client method; Raw and the probes never send them.
const (
	OpWirelessSet   Opcode = 0x4008
	OpClientListSet Opcode = 0x4013
)

// Read-only business opcodes.
const (
	OpWirelessGet              Opcode = 0x4009
	OpDeviceListGet            Opcode = 0x400F
	OpClientListGet            Opcode = 0x4012
	OpClientListSpeedGet       Opcode = 0x4014
	OpBlockedListGet           Opcode = 0x4018
	OpFWLatestGet              Opcode = 0x401C
	OpWANInfoGet               Opcode = 0x407B
	OpDeviceSignalLevelListGet Opcode = 0x4094
	OpDeviceResourceInfoGet    Opcode = 0x4317
	OpIPv4Get                  Opcode = 0x4004
	OpMLONetworkGet            Opcode = 0x43B0
	OpDeviceResourceListGet    Opcode = 0x431A
)

var opNames = map[Opcode]string{
	OpTokenAlloc: "TOKEN_ALLOC", OpTokenVerify: "TOKEN_VERIFY", OpTokenFree: "TOKEN_FREE",
	OpCompNegotiate: "COMP_NEGOTIATE", OpWirelessGet: "WIRELESS_GET", OpDeviceListGet: "DEVICE_LIST_GET",
	OpClientListGet: "CLIENT_LIST_GET", OpClientListSpeedGet: "CLIENT_LIST_SPEED_GET",
	OpBlockedListGet: "BLOCKED_LIST_GET", OpFWLatestGet: "FW_LATEST_GET", OpWANInfoGet: "WAN_INFO_GET",
	OpDeviceSignalLevelListGet: "DEVICE_SIGNAL_LEVEL_LIST_GET",
	OpDeviceResourceInfoGet:    "DEVICE_RESOURCE_INFO_GET",
	OpIPv4Get:                  "IPV4_GET",
	OpMLONetworkGet:            "MLO_NETWORK_GET",
	OpDeviceResourceListGet:    "DEVICE_RESOURCE_LIST_GET",
	OpWirelessSet:              "WIRELESS_SET",
	OpClientListSet:            "CLIENT_LIST_SET",
}

func (o Opcode) String() string {
	if n, ok := opNames[o]; ok {
		return n
	}
	return fmt.Sprintf("0x%04X", uint16(o))
}

// allowed is the complete set of opcodes Atlantis will put on the wire.
// Everything else is refused before framing (CLAUDE.md §12.3): writes are
// added here one at a time, only after they are confirmed on hardware, and
// WAN/LAN/DHCP/operation-mode/firmware/config-backup writes never are.
var allowed = map[Opcode]bool{
	OpTokenAlloc: true, OpTokenVerify: true, OpTokenFree: true, OpCompNegotiate: true,
	OpWirelessGet: true, OpDeviceListGet: true, OpClientListGet: true, OpClientListSpeedGet: true,
	OpBlockedListGet: true, OpFWLatestGet: true, OpWANInfoGet: true,
	OpDeviceSignalLevelListGet: true, OpDeviceResourceInfoGet: true,
	OpIPv4Get: true, OpMLONetworkGet: true, OpDeviceResourceListGet: true,
	// WIRELESS_SET: only through tmpclient.SetNetwork, which sends just the
	// network fields listed in its verifiedWrites (proven on a Deco M5).
	OpWirelessSet: true,
	// CLIENT_LIST_SET: only through tmpclient.RenameClient, which sends one
	// client's name the way the app's rename screen does.
	OpClientListSet: true,
}

// writes are the allowed opcodes that change device state.
var writes = map[Opcode]bool{OpWirelessSet: true, OpClientListSet: true}

// IsWrite reports whether op changes device state.
func IsWrite(op Opcode) bool { return writes[op] }

// Allowed reports whether op may be sent.
func Allowed(op Opcode) bool { return allowed[op] }

// ErrOpcodeNotAllowed is returned for any opcode outside the allowlist.
type ErrOpcodeNotAllowed struct{ Op Opcode }

func (e ErrOpcodeNotAllowed) Error() string {
	return fmt.Sprintf("tmp: opcode %s is not on the allowlist", e.Op)
}

// OpcodeByName looks up a known opcode by its app name, e.g. "DEVICE_LIST_GET".
func OpcodeByName(name string) (Opcode, bool) {
	for op, n := range opNames {
		if n == name {
			return op, true
		}
	}
	return 0, false
}
