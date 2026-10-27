package discovery

import (
	"encoding/json"
	"net/netip"
	"net/url"

	"github.com/A2MaTex/a2matex-runtime/internal/auth"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

type connectionDocument struct {
	APIBaseURL   string `json:"apiBaseUrl"`
	SessionToken string `json:"sessionToken"`
}

// encodeConnectionDocument creates the two values a local client needs to
// reach this Runtime process. Refusing non-loopback addresses prevents this
// discovery file from advertising an API exposed to the local network.
func encodeConnectionDocument(listenAddress string, session *auth.Session) ([]byte, error) {
	address, err := netip.ParseAddrPort(listenAddress)
	if err != nil || !address.Addr().IsLoopback() || address.Port() == 0 {
		return nil, xerror.ErrEncodeLocalAuthConnection
	}

	// url.URL formats IPv4 and IPv6 hosts correctly; string concatenation would
	// produce an invalid URL for an address such as [::1]:8765.
	document := connectionDocument{
		APIBaseURL: (&url.URL{
			Scheme: "http",
			Host:   address.String(),
			Path:   constants.LocalAPIBasePath,
		}).String(),
		SessionToken: session.ConnectionToken(),
	}

	encoded, err := json.Marshal(document)
	if err != nil {
		return nil, xerror.ErrEncodeLocalAuthConnection
	}

	return encoded, nil
}
