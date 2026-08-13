// Package motion implements motion and sound gating for recordings.
//
// It consumes go2rtc's JSON API (motion level per camera) and the mic
// daemon's PCM stream to decide when an event starts and ends, and hands
// triggered segments to the recorder. Mirrors v1's motion.py contract
// including event filename timestamps and per-camera unique mic ports.
package motion
