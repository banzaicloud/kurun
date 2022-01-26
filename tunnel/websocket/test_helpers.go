package websocket

import "sync"

const MaxUint = ^uint(0)
const MinUint = 0
const MaxInt = int(MaxUint >> 1)
const MinInt = -MaxInt - 1

type ConcurrencyCounter struct {
	count int
	min   int
	max   int
	mutex sync.Mutex
}

func NewCounter() *ConcurrencyCounter {
	cc := &ConcurrencyCounter{}
	cc.Init(0)
	return cc
}

func (c *ConcurrencyCounter) Init(val int) {
	c.mutex.Lock()
	c.count = val
	c.min = MaxInt
	c.max = MinInt
	c.mutex.Unlock()
}

func (c *ConcurrencyCounter) Set(val int) {
	c.mutex.Lock()
	c.count = val
	c.update()
	c.mutex.Unlock()
}

func (c *ConcurrencyCounter) Inc() {
	c.mutex.Lock()
	c.count++
	c.update()
	c.mutex.Unlock()
}

func (c *ConcurrencyCounter) Dec() {
	c.mutex.Lock()
	c.count--
	c.update()
	c.mutex.Unlock()
}

func (c *ConcurrencyCounter) Count() int {
	return c.count
}

func (c *ConcurrencyCounter) Max() int {
	return c.max
}

func (c *ConcurrencyCounter) Min() int {
	return c.min
}

func (c *ConcurrencyCounter) update() {
	if c.count > c.max {
		c.max = c.count
	}
	if c.count < c.min {
		c.min = c.count
	}
}
