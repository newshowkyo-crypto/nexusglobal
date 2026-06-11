package validator

import (
	"regexp"
	"strings"
)

type Network string

const (
	NetworkTRONTRC20 Network = "TRON_TRC20"
	NetworkETHERC20  Network = "ETH_ERC20"
	NetworkBTC       Network = "BTC"
	NetworkSOL       Network = "SOL"
)

var (
	tronAddr = regexp.MustCompile(`^T[a-zA-Z0-9]{33}$`)
	ethAddr  = regexp.MustCompile(`^0x[0-9a-fA-F]{40}$`)
	btcAddr  = regexp.MustCompile(`^(bc1[a-zA-HJ-NP-Z0-9]{25,62}|[13][a-km-zA-HJ-NP-Z1-9]{25,34})$`)
	solAddr  = regexp.MustCompile(`^[1-9A-HJ-NP-Za-km-z]{32,44}$`)
)

func ValidateAddress(network Network, address string) (bool, string) {
	addr := strings.TrimSpace(address)
	if addr == "" {
		return false, "address is empty"
	}

	switch network {
	case NetworkTRONTRC20:
		if tronAddr.MatchString(addr) {
			return true, ""
		}
		return false, "invalid TRON TRC20 address format"
	case NetworkETHERC20:
		if ethAddr.MatchString(addr) {
			return true, ""
		}
		return false, "invalid ETH ERC20 address format"
	case NetworkBTC:
		if btcAddr.MatchString(addr) {
			return true, ""
		}
		return false, "invalid BTC address format"
	case NetworkSOL:
		if solAddr.MatchString(addr) {
			return true, ""
		}
		return false, "invalid SOL address format"
	default:
		return false, "unsupported network: " + string(network)
	}
}
