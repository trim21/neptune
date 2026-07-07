package client

func (c *Client) startUploadPool() {
	workerCount := int(c.session.Config.App.GlobalUploadSlots)
	if workerCount <= 0 {
		workerCount = 64
	}
	workerCount = min(workerCount, 4096)

	queueCap := workerCount * 256
	queueCap = min(queueCap, 65536)
	queueCap = max(queueCap, 1024)

	c.session.UploadQ = make(chan func(), queueCap)

	for range workerCount {
		go c.uploadWorker()
	}
}

func (c *Client) uploadWorker() {
	for {
		select {
		case <-c.session.Ctx.Done():
			return
		case fn := <-c.session.UploadQ:
			fn()
		}
	}
}
