// Package agentgate exposes module-level assets shared by internal packages.
package agentgate

import "embed"

// InstallAssets contains hook and service templates used by the install command.
//
//go:embed hooks/*.json hooks/*.toml packaging/macos/*.in packaging/systemd/*.in
var InstallAssets embed.FS
