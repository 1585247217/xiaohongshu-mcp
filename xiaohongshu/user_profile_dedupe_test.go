package xiaohongshu

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestUniqueProfileFeedsPreservesVisibleOrder(t *testing.T) {
	feeds := []Feed{{ID: "a"}, {ID: "b"}, {ID: "a"}, {ID: ""}, {ID: ""}}
	got, removed := uniqueProfileFeeds(feeds)
	assert.Equal(t, []Feed{{ID: "a"}, {ID: "b"}, {ID: ""}, {ID: ""}}, got)
	assert.Equal(t, 1, removed)
}
