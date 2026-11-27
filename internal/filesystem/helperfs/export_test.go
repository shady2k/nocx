package helperfs

// SetWatchRetryClock installs a fake clock before the provider starts watching.
func SetWatchRetryClock(p *Provider, clock watchRetryClock) {
	p.watchRetryClock = clock
}
