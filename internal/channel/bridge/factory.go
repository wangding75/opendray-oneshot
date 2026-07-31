package bridge

import "github.com/opendray/opendray-v2/internal/channel"

func init() {
	channel.Register("bridge", Factory)
}
