// SPDX-License-Identifier: GPL-3.0-or-later

package wa

import (
	"context"
	"errors"
	"time"

	"golang.org/x/sys/unix"
)

// watchNetwork calls changed whenever the kernel reports a change of
// network links, addresses or routes: a cable plugged in, Wi-Fi joined, a
// laptop resumed. It listens on a netlink route socket, which costs no
// network traffic. Bursts of changes call changed once. It returns when
// ctx ends, or at once if the socket cannot be opened.
func watchNetwork(ctx context.Context, changed func()) error {
	fd, err := unix.Socket(unix.AF_NETLINK, unix.SOCK_RAW|unix.SOCK_CLOEXEC, unix.NETLINK_ROUTE)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	sa := &unix.SockaddrNetlink{Family: unix.AF_NETLINK, Groups: unix.RTMGRP_LINK | unix.RTMGRP_IPV4_IFADDR |
		unix.RTMGRP_IPV6_IFADDR | unix.RTMGRP_IPV4_ROUTE | unix.RTMGRP_IPV6_ROUTE}
	if err := unix.Bind(fd, sa); err != nil {
		return err
	}
	// Wake up every second to notice ctx ending.
	tv := unix.Timeval{Sec: 1}
	if err := unix.SetsockoptTimeval(fd, unix.SOL_SOCKET, unix.SO_RCVTIMEO, &tv); err != nil {
		return err
	}
	buf := make([]byte, 1<<16)
	var pending bool
	var last time.Time
	for ctx.Err() == nil {
		_, _, err := unix.Recvfrom(fd, buf, 0)
		switch {
		case err == nil:
			pending = true
		case errors.Is(err, unix.EAGAIN), errors.Is(err, unix.EINTR):
		default:
			return err
		}
		// Coalesce a burst: report once the network has been quiet for a
		// moment, at most once a second.
		if pending && time.Since(last) > time.Second {
			pending, last = false, time.Now()
			changed()
		}
	}
	return nil
}
