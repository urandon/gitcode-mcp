# Admin UI third-party notices

The embedded admin UI is built from the dependency versions locked in `package-lock.json`.
The release gate checks every locked package and currently permits only MIT, ISC,
Apache-2.0, and BSD-3-Clause dependencies.

Production-visible code includes Svelte/SvelteKit runtime code under MIT and Lucide
icons under ISC. Build and test dependencies are also included in the automated
license scan even though they are not embedded in the Go binary. Upstream copyright
and license texts remain available in each locked package and its published source.
