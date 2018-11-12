package prbuild

// Lifted from https://golang.org/pkg/container/heap/

import (
	"container/heap"

	"github.com/brinick/ali-ci/pullrequest"
	"github.com/brinick/logging"
)

// A priorityQueueItem is something we manage in a priority queue.
type priorityQueueItem struct {
	pr       *pullrequest.PR
	priority int
	index    int // The index of the item in the heap.
}

// A priorityQueue implements heap.Interface and holds Items.
type priorityQueue []*priorityQueueItem

func (pq *priorityQueue) Len() int {
	return len(*pq)
}

func (pq *priorityQueue) Less(i, j int) bool {
	// We want Pop to give us the highest,
	// not lowest, priority so we use greater than here.
	return (*pq)[i].priority > (*pq)[j].priority
}

func (pq *priorityQueue) Swap(i, j int) {
	(*pq)[i], (*pq)[j] = (*pq)[j], (*pq)[i]
	(*pq)[i].index = i
	(*pq)[j].index = j
}

func (pq *priorityQueue) PushAll(items []*priorityQueueItem) {
	for _, item := range items {
		pq.Push(item)
	}
}

func (pq *priorityQueue) Push(x interface{}) {
	n := len(*pq)
	item := x.(*priorityQueueItem)
	item.index = n
	*pq = append(*pq, item)
}

func (pq *priorityQueue) Pop() interface{} {
	old := *pq
	n := len(old)
	item := old[n-1]
	item.index = -1 // for safety
	*pq = old[0 : n-1]
	return item
}

func (pq *priorityQueue) Empty() bool {
	if pq == nil {
		return true
	}
	return pq.Len() == 0
}

func (pq *priorityQueue) Next() interface{} {
	return (*pq)[0]
}

// update modifies the priority and value of an Item in the queue.
func (pq *priorityQueue) update(item *priorityQueueItem, value *pullrequest.PR, priority int) {
	item.pr = value
	item.priority = priority
	heap.Fix(pq, item.index)
}

// createPriorityQueue will create a priority queue
// with an initial capacity of the number of passed in items.
func createPriorityQueue(items []*priorityQueueItem) *priorityQueue {
	Error("Let's create a priorityQueue")
	pqueue := priorityQueue(items)
	// pqueue := make(priorityQueue, len(items))
	//pqueue = append(pqueue, items...)
	Error("createPriorityQueue added items", logging.F("n", pqueue.Len()))
	/*
		for i, item := range items {
			pqueue[i] = item
		}
	*/
	heap.Init(&pqueue)
	return &pqueue
}
