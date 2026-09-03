package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestXHSNoteURLsOnlyReturnsXHSLinksAndDedupes(t *testing.T) {
	got := xhsNoteURLs("看 https://xhslink.cn/o/abc 。还有 https://xhslink.cn/o/abc 和 https://example.com/no")
	assert.Equal(t, []string{"https://xhslink.cn/o/abc"}, got)
}
