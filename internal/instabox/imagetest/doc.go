// Package imagetest checks a running LydiaBox InstaBox image
// (deploy/instabox) over the real host link: deploy/instabox/test.sh runs
// its test with the link URL and secret of the container it started. The
// test skips when LYDIABOX_IMAGE_LINK_URL is not set. With a real
// machine, set LYDIABOX_IMAGE_LINK_URL to the wss:// URL of the private
// port, LYDIABOX_IMAGE_LINK_TOKEN to its token, and
// LYDIABOX_IMAGE_LINK_SECRET to the link secret (docs/instabox.md).
package imagetest
