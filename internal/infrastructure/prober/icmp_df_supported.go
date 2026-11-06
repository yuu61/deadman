//go:build linux

package prober

// nativeDFSupported reports whether a direct echo may set do-not-fragment: only Linux
// (icmp_df_linux.go), whose error queue reports a path MTU error as unknown instead of loss.
const nativeDFSupported = true
