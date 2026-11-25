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
)

var opNames = map[Opcode]string{
	OpTokenAlloc: "TOKEN_ALLOC", OpTokenVerify: "TOKEN_VERIFY", OpTokenFree: "TOKEN_FREE",
	OpCompNegotiate: "COMP_NEGOTIATE", OpWirelessGet: "WIRELESS_GET", OpDeviceListGet: "DEVICE_LIST_GET",
	OpClientListGet: "CLIENT_LIST_GET", OpClientListSpeedGet: "CLIENT_LIST_SPEED_GET",
	OpBlockedListGet: "BLOCKED_LIST_GET", OpFWLatestGet: "FW_LATEST_GET", OpWANInfoGet: "WAN_INFO_GET",
	OpDeviceSignalLevelListGet: "DEVICE_SIGNAL_LEVEL_LIST_GET",
	OpDeviceResourceInfoGet:    "DEVICE_RESOURCE_INFO_GET",
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
}

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
