//go:build !linux && !darwin && !freebsd

package prober

// nativeDFSupported is false where no do-not-fragment socket option is known to work; a
// direct row with df=on is refused when it is built. The tags are the exact complement of
// icmp_df_supported.go.
const nativeDFSupported = false
