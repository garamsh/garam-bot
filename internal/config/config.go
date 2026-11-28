// Package config resolves the workspace's settings through one precedence, flags, then
// GARAM_BOT_* environment variables, then the config file, then defaults, and refuses a config file
// another user can read (docs/architecture/security.md).
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/go-viper/mapstructure/v2"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"
)

// defaultWorkspaceAddr is the gRPC endpoint of a workspace (ADR 0006): plain host:port, no scheme,
// and the workspace listener is unauthenticated in this phase.
const defaultWorkspaceAddr = "127.0.0.1:8081"

// AddSharedFlags registers the flags every subcommand resolves through,
// on fs. The flag layer is the top of this package's precedence, so the
// flags it reads are registered here rather than at the call site: a
// resolution the caller can only reach with the right flag set is one
// the caller can get wrong.
func AddSharedFlags(fs *pflag.FlagSet) {
	fs.String("config", "", "config file path (default: ./configs/workspace.yaml, then the XDG config dir)")
	fs.Bool(strictConfigFlag, false, "refuse to start when the config file carries a key no setting reads")
}

// strictConfigFlag turns the unknown-key warning into a startup failure.
// It is read off the flag set alone, never through viper, so neither the
// environment nor the file it checks can switch it off.
const strictConfigFlag = "strict-config"

// newConfigViper returns a viper with the shared resolution layer both
// entry-point families use: GARAM_BOT_* environment variables with dashes
// and dots mapped to underscores.
func newConfigViper() *viper.Viper {
	v := viper.New()
	v.SetEnvPrefix("GARAM_BOT")
	v.SetEnvKeyReplacer(strings.NewReplacer("-", "_", ".", "_"))
	v.AutomaticEnv()
	return v
}

// readConfigFile loads the config file into v following the resolution
// order: an explicit --config path, then ./configs/<cwdName>.yaml, then
// os.UserConfigDir()/garam-bot/<xdgName> (XDG; ~/.config/garam-bot/ on Linux).
// A missing file is an error only for the explicit flag path. A loaded
// file must be owner-only (docs/architecture/security.md). It returns
// the path it loaded, or "" when no file exists.
func readConfigFile(v *viper.Viper, flagPath, cwdName, xdgName string) (string, error) {
	loaded, err := loadConfigFile(v, flagPath, cwdName, xdgName)
	if err != nil {
		return "", err
	}
	if loaded != "" {
		if err := RequireOwnerOnly("config file", loaded); err != nil {
			return "", err
		}
	}
	return loaded, nil
}

// recordKeys makes Unmarshal record which input keys it consumed, so
// the ones it did not can be reported.
func recordKeys(md *mapstructure.Metadata) viper.DecoderConfigOption {
	return func(dc *mapstructure.DecoderConfig) { dc.Metadata = md }
}

// reportUnknownKeys warns for every key the config file carries that no
// setting reads: a typo, a renamed key, or a block left over from a
// removed feature. By default it reports rather than refuses, because
// refusing would reject an operator's whole file the moment a key is
// renamed. Under --strict-config it refuses instead, so a process that
// started has read every key its file carries.
//
// The warning names the key and the file, which is what makes it
// actionable: `tools.pin` for `tools.pins` turns drift pinning off
// while the registry reports it as never configured (ADR 0006
// invariant 2), and only the key's own name separates the two.
//
// unused carries every input key Unmarshal left over, which includes
// the command's flags — they are bound to the same viper and most name
// no setting. Only the keys the file itself carries are the operator's
// to fix, so the file is what they are filtered against.
func reportUnknownKeys(fs *pflag.FlagSet, loaded string, unused []string) error {
	strict, err := fs.GetBool(strictConfigFlag)
	if err != nil {
		return err
	}
	if loaded == "" || len(unused) == 0 {
		return nil
	}
	fileKeys := configFileKeys(loaded)
	if len(fileKeys) == 0 {
		return nil
	}
	unknown := make([]string, 0, len(unused))
	for _, key := range unused {
		if carriedBy(fileKeys, key) {
			unknown = append(unknown, key)
		}
	}
	sort.Strings(unknown)
	if strict && len(unknown) > 0 {
		return fmt.Errorf("config: %s carries %s, which no setting reads; --%s refuses to start on an unread key",
			loaded, strings.Join(unknown, ", "), strictConfigFlag)
	}
	for _, key := range unknown {
		slog.Warn("unknown config key: nothing reads it",
			"op", "config.load", "key", key, "file", loaded)
	}
	return nil
}

// carriedBy reports whether the config file holds key, either as a leaf
// of its own or as the root of a section: an unread `console:` section
// is left over as `console`, while the file spells its leaf
// `console.url`.
func carriedBy(fileKeys []string, key string) bool {
	for _, fileKey := range fileKeys {
		if fileKey == key || strings.HasPrefix(fileKey, key+".") {
			return true
		}
	}
	return false
}

// configFileKeys returns the keys the config file itself carries,
// separately from the defaults, flags and environment merged alongside
// them. A file that no longer parses yields none: it was read once
// already, and a diagnostic is not the place to fail on a second read.
func configFileKeys(path string) []string {
	v := viper.New()
	v.SetConfigFile(path)
	if err := v.ReadInConfig(); err != nil {
		return nil
	}
	return v.AllKeys()
}

// loadConfigFile reads the config file into v and returns the path it
// actually loaded, or "" when no file exists (viper's ConfigFileUsed
// reports attempted paths too, so it cannot answer this).
func loadConfigFile(v *viper.Viper, flagPath, cwdName, xdgName string) (string, error) {
	if flagPath != "" {
		v.SetConfigFile(flagPath)
		return flagPath, v.ReadInConfig()
	}
	v.SetConfigName(cwdName)
	v.AddConfigPath("./configs")
	var notFound viper.ConfigFileNotFoundError
	if err := v.ReadInConfig(); !errors.As(err, &notFound) {
		return v.ConfigFileUsed(), err
	}
	configDir, err := os.UserConfigDir()
	if err != nil {
		return "", nil
	}
	path := filepath.Join(configDir, "garam-bot", xdgName)
	v.SetConfigFile(path)
	if err := v.ReadInConfig(); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", nil
		}
		return "", err
	}
	return path, nil
}
