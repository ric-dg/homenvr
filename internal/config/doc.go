// Package config loads, validates and hot-reloads the JSONC configuration.
//
// Mirrors v1's config.py contract: cameras, per-camera video/audio settings,
// recording mode, retention, paths and external tool locations. The schema is
// defined once in this package and is the single source of truth for the CLI,
// control panel and the supervisor.
//
// Loading semantics match v1 exactly: the user config (a JSONC file) is merged
// over built-in defaults, missing fields keep their defaults, and each camera
// entry is completed against a per-camera template. Unknown keys are ignored,
// a broken edit never clobbers the last good config, and tool locations are
// resolved (configured path, bundled copy, PATH, scoop). Verified against
// `python config.py --dump` — dumps are structurally identical.
package config
