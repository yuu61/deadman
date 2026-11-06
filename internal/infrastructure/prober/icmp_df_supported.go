//go:build linux || darwin || freebsd

package prober

// nativeDFSupported reports whether this OS can send a direct echo with the do-not-fragment
// bit set: Linux through icmp_df_linux.go, Darwin and FreeBSD through icmp_df_bsd.go.
const nativeDFSupported = true
