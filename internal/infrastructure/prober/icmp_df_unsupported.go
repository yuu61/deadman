//go:build !linux

package prober

// nativeDFSupported is false where a direct echo does not read the ICMP error a router
// returns for an oversized do-not-fragment echo; that echo would time out and count as loss,
// so a direct row with df=on is refused when it is built. The tags are the exact complement
// of icmp_df_supported.go.
const nativeDFSupported = false
