package kube

import (
	"context"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
)

type EventType string

const (
	EventAdded   EventType = "added"
	EventUpdated EventType = "updated"
	EventRemoved EventType = "removed"
)

type WorkloadEvent struct {
	Type     EventType
	Workload Workload
	At       time.Time
}

type Watcher struct {
	factory informers.SharedInformerFactory
	events  chan WorkloadEvent
	resync  time.Duration
}

func NewWatcher(client kubernetes.Interface, resync time.Duration, buffer int) *Watcher {
	if buffer <= 0 {
		buffer = 256
	}

	return &Watcher{
		factory: informers.NewSharedInformerFactory(client, resync),
		events:  make(chan WorkloadEvent, buffer),
		resync:  resync,
	}
}

func (w *Watcher) Events() <-chan WorkloadEvent {
	return w.events
}

func (w *Watcher) Run(ctx context.Context) error {
	apps := w.factory.Apps().V1()

	registrations := []struct {
		informer cache.SharedIndexInformer
		convert  func(any) (Workload, bool)
	}{
		{apps.Deployments().Informer(), convertDeployment},
		{apps.StatefulSets().Informer(), convertStatefulSet},
		{apps.DaemonSets().Informer(), convertDaemonSet},
	}

	for _, registration := range registrations {
		if err := w.register(ctx, registration.informer, registration.convert); err != nil {
			return err
		}
	}

	w.factory.Start(ctx.Done())

	for informerType, synced := range w.factory.WaitForCacheSync(ctx.Done()) {
		if !synced {
			return fmt.Errorf("informer cache for %s did not sync", informerType)
		}
	}

	<-ctx.Done()
	close(w.events)

	return nil
}

func (w *Watcher) register(ctx context.Context, informer cache.SharedIndexInformer, convert func(any) (Workload, bool)) error {
	_, err := informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj any) {
			w.emit(ctx, EventAdded, convert, obj)
		},
		UpdateFunc: func(_, obj any) {
			w.emit(ctx, EventUpdated, convert, obj)
		},
		DeleteFunc: func(obj any) {
			if tombstone, ok := obj.(cache.DeletedFinalStateUnknown); ok {
				obj = tombstone.Obj
			}
			w.emit(ctx, EventRemoved, convert, obj)
		},
	})
	if err != nil {
		return fmt.Errorf("register informer handler: %w", err)
	}

	return nil
}

func (w *Watcher) emit(ctx context.Context, eventType EventType, convert func(any) (Workload, bool), obj any) {
	workload, ok := convert(obj)
	if !ok {
		return
	}

	event := WorkloadEvent{Type: eventType, Workload: workload, At: time.Now()}

	select {
	case w.events <- event:
	case <-ctx.Done():
	}
}

func convertDeployment(obj any) (Workload, bool) {
	deployment, ok := obj.(*appsv1.Deployment)
	if !ok {
		return Workload{}, false
	}
	return WorkloadFromDeployment(deployment), true
}

func convertStatefulSet(obj any) (Workload, bool) {
	set, ok := obj.(*appsv1.StatefulSet)
	if !ok {
		return Workload{}, false
	}
	return WorkloadFromStatefulSet(set), true
}

func convertDaemonSet(obj any) (Workload, bool) {
	set, ok := obj.(*appsv1.DaemonSet)
	if !ok {
		return Workload{}, false
	}
	return WorkloadFromDaemonSet(set), true
}
