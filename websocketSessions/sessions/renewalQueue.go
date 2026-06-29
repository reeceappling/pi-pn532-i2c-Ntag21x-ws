package sessions

import (
	"context"
	"sync"
)

type RenewalQueue2 struct {
	head, tail *RenewalItem
	*sync.RWMutex
}

func (rq *RenewalQueue2) RemoveHead() {
	rq.Lock()
	defer rq.Unlock()
	if rq.head == rq.tail {
		rq.head, rq.tail = nil, nil
		return
	}
	newHead := rq.head.next
	newHead.SetPrev(nil)
	rq.head = newHead
}
func (rq *RenewalQueue2) MoveHeadToTail() {
	rq.Lock()
	defer rq.Unlock()
	if rq.head == rq.tail {
		// Do nothing
		return
	}
	currentHead, currentTail, newHead := rq.head, rq.tail, rq.head.next
	newHead.SetPrev(nil)
	currentHead.SetNext(nil)
	currentTail.SetNext(currentHead)
	rq.head, rq.tail = newHead, currentHead
}
func (rq *RenewalQueue2) Append(item *RenewalItem) {
	rq.Lock()
	defer rq.Unlock()
	if rq.head == nil {
		rq.head, rq.tail = item, item
	}
	currentTail := rq.tail
	if item == currentTail {
		return // Do nothing
	}
	currentTail.SetNext(item)
	item.Set(currentTail, nil)

	itemBefore := item.prev
	itemAfter := item.next
	if itemBefore != nil {
		itemBefore.next = itemAfter
	}
	currentTail.next = item
	rq.tail = item
}

func (rq *RenewalQueue2) Run(ctx context.Context) { // TODO: use?
	go func() {
		_ = <-ctx.Done()
		rq.Dispose()
	}()
}

func (rq *RenewalQueue2) Dispose() {
	rq.head, rq.tail = nil, nil
}

type RenewalQueue struct {
	head, tail *RenewalItem
	*sync.RWMutex
}

func (rq *RenewalQueue2) Head() *RenewalItem {
	rq.RLock()
	defer rq.RUnlock()
	return rq.head
}
func (rq *RenewalQueue) BumpToEnd(item *RenewalItem) { // TODO: fully ensure working as intended
	rq.RLock()
	if item == rq.tail {
		return // Do nothing
	}
	if item == rq.head {
		rq.head = item.next
	}
	currentTail := rq.tail
	itemBefore, itemAfter := item.prev, item.next
	itemBefore.SetNext(itemAfter)
	itemAfter.SetPrev(itemBefore)
	item.Set(currentTail, nil)
	currentTail.SetNext(item)
	rq.tail = item
}
func (rq *RenewalQueue) RemoveHead() {
	if rq.head == rq.tail {
		rq.head, rq.tail = nil, nil
		return
	}
	newHead := rq.head.next
	newHead.SetPrev(nil)
	rq.head = newHead
}
func (rq *RenewalQueue) MoveHeadToTail() {
	if rq.head == rq.tail {
		// Do nothing
		return
	}
	currentHead, currentTail, newHead := rq.head, rq.tail, rq.head.next
	newHead.SetPrev(nil)
	currentHead.SetNext(nil)
	currentTail.SetNext(currentHead)
	rq.head, rq.tail = newHead, currentHead
}
func (rq *RenewalQueue2) RemoveItem(item *RenewalItem) {
	rq.Lock()
	defer rq.Unlock()
	isTail := item == rq.tail
	if item == rq.head {
		rq.head = item.next
		if isTail {
			rq.tail = nil
		}
		return
	}
	if isTail {
		rq.tail = rq.tail.prev
		return
	}
	// If item is between head and tail
	prevItem, nextItem := item.prev, item.next
	prevItem.next = nextItem
	nextItem.prev = prevItem
}
func (rq *RenewalQueue) Append(item *RenewalItem) {
	if rq.head == nil {
		//item.prev, item.next = nil, nil // TODO: no!
		rq.head, rq.tail = item, item
	}
	currentTail := rq.tail
	if item == currentTail {
		return // Do nothing
	}
	currentTail.SetNext(item)
	item.Set(currentTail, nil)

	itemBefore := item.prev
	itemAfter := item.next
	if itemBefore != nil {
		itemBefore.next = itemAfter
	}
	currentTail.next = item
	rq.tail = item
}

type RenewalItem struct { // TODO: always used as a ptr
	prev, next *RenewalItem
	Sess       *Session
}

func (ri *RenewalItem) SetPrev(target *RenewalItem) {
	if ri == nil {
		return
	}
	ri.prev = target
}

func (ri *RenewalItem) SetNext(target *RenewalItem) {
	if ri == nil {
		return
	}
	ri.next = target
}
func (ri *RenewalItem) Set(prev, next *RenewalItem) {
	if ri == nil {
		return
	}
	ri.prev, ri.next = prev, next
}
