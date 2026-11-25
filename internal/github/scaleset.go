package github

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/actions/scaleset"
	"github.com/robertjndw/oakestra-github-runner/internal/core"
)

// SystemName identifies this addon to the scale set service (sent in the User-Agent).
const SystemName = "oakestra-github-runner"

// ScaleSetClientFactory builds runner scale set clients from stored connections.
type ScaleSetClientFactory struct {
	// Version is reported in the scale set client's system info.
	Version string
	// HTTPClient is optional. Nil uses the scaleset package's default client, which already
	// has timeouts tuned for its long poll.
	HTTPClient *http.Client
	Logger     *slog.Logger
}

// NewScaleSetClientFactory returns a factory reporting version to GitHub.
func NewScaleSetClientFactory(version string, logger *slog.Logger) *ScaleSetClientFactory {
	return &ScaleSetClientFactory{Version: version, Logger: logger}
}

// NewClient builds a client for a ready connection. It does not contact GitHub; bad
// credentials surface on the first call.
func (f *ScaleSetClientFactory) NewClient(conn core.Connection) (*scaleset.Client, error) {
	if conn.Status != core.ConnectionReady {
		return nil, fmt.Errorf("connection %q is not ready (status %s)", conn.Name, conn.Status)
	}
	cu, err := ParseConfigURL(conn.ConfigURL)
	if err != nil {
		return nil, err
	}
	info := scaleset.SystemInfo{
		System:    SystemName,
		Version:   f.Version,
		Subsystem: "controller",
	}
	var opts []scaleset.HTTPOption
	if f.HTTPClient != nil {
		opts = append(opts, scaleset.WithHTTPClient(f.HTTPClient))
	}
	if f.Logger != nil {
		opts = append(opts, scaleset.WithLogger(f.Logger))
	}

	switch conn.AuthKind {
	case core.AuthApp:
		// The scaleset client signs its JWT with this value as the issuer, and GitHub accepts
		// either the client id or the numeric App id there. Client id is GitHub's recommended
		// form, the App id covers connections created before we stored it.
		clientID := conn.AppClientID
		if clientID == "" && conn.AppID != 0 {
			clientID = strconv.FormatInt(conn.AppID, 10)
		}
		if clientID == "" || conn.InstallationID == 0 || conn.Secret == "" {
			return nil, fmt.Errorf("connection %q is missing App id, installation or private key", conn.Name)
		}
		return scaleset.NewClientWithGitHubApp(scaleset.ClientWithGitHubAppConfig{
			GitHubConfigURL: cu.String(),
			GitHubAppAuth: scaleset.GitHubAppAuth{
				ClientID:       clientID,
				InstallationID: conn.InstallationID,
				PrivateKey:     conn.Secret,
			},
			SystemInfo: info,
		}, opts...)
	case core.AuthPAT:
		if conn.Secret == "" {
			return nil, fmt.Errorf("connection %q has no token", conn.Name)
		}
		return scaleset.NewClientWithPersonalAccessToken(scaleset.NewClientWithPersonalAccessTokenConfig{
			GitHubConfigURL:     cu.String(),
			PersonalAccessToken: conn.Secret,
			SystemInfo:          info,
		}, opts...)
	default:
		return nil, errors.New("unknown connection auth kind " + string(conn.AuthKind))
	}
}
