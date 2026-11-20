package vmm

// Remote VMs (Capabilities.Remote and SelfPause).
//
// The controller was written for a local VM: only this process changes its
// power state, so the cached state is the truth. A remote machine differs
// in three ways, and the controller handles each one:
//
//   - Its boot continues while this process restarts. Refresh keeps a
//     remote StateBooting, and bootLocked waits for the guest instead of
//     creating the machine again.
//   - Its guest is reached over a network link that can be slow to wake.
//     Capabilities.ReadyProbeTimeout sets the bound of one readiness probe.
//   - The provider can pause or resume it without this process. The driver
//     reports such a change through StateWatcher, and Observe updates the
//     cached state, the idle timer, and the OnStateChange callback. Until
//     the driver reports a self-pause, calls fail fast with ErrUnavailable
//     from the driver's DialGuest; they do not hang.
//   - Its provider can need minutes for a start or resume.
//     Capabilities.ProviderTimeout bounds that part of a boot, and a boot
//     that fails never stops the machine (cleanupFailedBoot). When the
//     machine runs later, Observe adopts it and clears the last error.

// Observe records a power change that the driver saw but did not make
// (for example a provider pause or resume). The change is ignored while
// this controller runs its own transition (boot, hibernate, or stop),
// because that transition reads the driver state itself.
func (c *Controller) Observe(tid string, st State) {
	if st == StateBooting {
		return
	}
	c.mu.Lock()
	t := c.tenant(tid)
	busy := t.boot != nil || t.pausing
	c.mu.Unlock()
	if busy || !t.trans.TryLock() {
		return
	}
	defer t.trans.Unlock()
	c.setState(tid, t, st)
	if st == StateRunning {
		// The machine runs: an earlier failed boot (for example a resume
		// that took longer than the provider bound) is no longer true.
		c.clearError(t)
	}
}
