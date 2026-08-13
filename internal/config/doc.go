// Package config loads, validates and hot-reloads the JSONC configuration.
//
// Planned to mirror v1's config.py contract: cameras, per-camera video/audio
// settings, recording mode, retention, paths and external tool locations.
// Schema is defined once and is the single source of truth for CLI, control
// panel and the supervisor.
package config
