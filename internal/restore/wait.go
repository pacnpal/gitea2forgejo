package restore

import "time"

func timeSleep(seconds int) {
	time.Sleep(time.Duration(seconds) * time.Second)
}
