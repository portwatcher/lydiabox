//go:build linux

package guestfs

import (
	"fmt"
	"os"
	"runtime"

	"golang.org/x/sys/unix"
)

const canSwitchIdentity = true

// rootGroups are the supplementary groups of the daemon, restored after
// every switch.
var rootGroups, rootGroupsErr = unix.Getgroups()

// runAs runs fn on a locked OS thread whose file system identity
// (setfsuid, setfsgid, and supplementary groups) is the agent user. These
// calls change only the calling thread on Linux. A change from fsuid 0 to
// another fsuid also clears the file capabilities (CAP_DAC_OVERRIDE,
// CAP_FOWNER, ...) of the thread, so the kernel applies the agent user's
// permissions. When the identity cannot be restored, the thread stays
// locked and exits with the goroutine.
func runAs(id *Identity, fn func() error) error {
	if rootGroupsErr != nil {
		return fmt.Errorf("guestfs: read supplementary groups: %w", rootGroupsErr)
	}
	runtime.LockOSThread()
	if err := switchTo(id); err != nil {
		if restore() == nil {
			runtime.UnlockOSThread()
		}
		return err
	}
	err := fn()
	if restore() == nil {
		runtime.UnlockOSThread()
	}
	return err
}

func switchTo(id *Identity) error {
	groups := id.Groups
	if groups == nil {
		groups = []int{id.GID}
	}
	if err := unix.Setgroups(groups); err != nil {
		return fmt.Errorf("guestfs: setgroups: %w", err)
	}
	unix.Setfsgid(id.GID)
	unix.Setfsuid(id.UID)
	if cur, _ := unix.SetfsuidRetUid(-1); cur != id.UID {
		return fmt.Errorf("guestfs: setfsuid(%d) did not take effect (fsuid %d)", id.UID, cur)
	}
	if cur, _ := unix.SetfsgidRetGid(-1); cur != id.GID {
		return fmt.Errorf("guestfs: setfsgid(%d) did not take effect (fsgid %d)", id.GID, cur)
	}
	return nil
}

func restore() error {
	unix.Setfsuid(os.Geteuid())
	unix.Setfsgid(os.Getegid())
	if err := unix.Setgroups(rootGroups); err != nil {
		return err
	}
	if cur, _ := unix.SetfsuidRetUid(-1); cur != os.Geteuid() {
		return fmt.Errorf("guestfs: fsuid not restored")
	}
	return nil
}
