package db

import (
	"fmt"
	"net"
	"strings"

	"github.com/2HgO/quidax-go/config"
	tdb "github.com/tigerbeetle/tigerbeetle-go"
	tdb_types "github.com/tigerbeetle/tigerbeetle-go/pkg/types"
)

func GetTxDBConnection() (tdb.Client, error) {
	addresses, err := resolveAddresses(config.TX_DB_URL)
	if err != nil {
		return nil, err
	}

	client, err := tdb.NewClient(tdb_types.ToUint128(0), addresses)
	if err != nil {
		return nil, fmt.Errorf("connect to TigerBeetle: %w", err)
	}

	return client, nil
}

// TigerBeetle's client accepts IP addresses, while Compose service discovery
// exposes services by DNS name. Resolve hostnames at startup and preserve
// literal IPv4/IPv6 addresses unchanged.
func resolveAddresses(value string) ([]string, error) {
	configured := strings.Split(value, ",")
	addresses := make([]string, 0, len(configured))

	for _, configuredAddress := range configured {
		configuredAddress = strings.TrimSpace(configuredAddress)
		host, port, err := net.SplitHostPort(configuredAddress)
		if err != nil {
			return nil, fmt.Errorf("invalid TigerBeetle address %q: %w", configuredAddress, err)
		}

		if net.ParseIP(host) != nil {
			addresses = append(addresses, configuredAddress)
			continue
		}

		ips, err := net.LookupIP(host)
		if err != nil {
			return nil, fmt.Errorf("resolve TigerBeetle host %q: %w", host, err)
		}
		if len(ips) == 0 {
			return nil, fmt.Errorf("resolve TigerBeetle host %q: no addresses found", host)
		}

		selected := ips[0]
		for _, ip := range ips {
			if ip.To4() != nil {
				selected = ip
				break
			}
		}
		addresses = append(addresses, net.JoinHostPort(selected.String(), port))
	}

	return addresses, nil
}
