package nodelock

import (
	"context"
	"fmt"
	"time"

	"4pd.io/k8s-vgpu/pkg/util/client"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
)

const (
	NodeLockTime = "4pd.io/mutex.lock"
	MaxLockRetry = 5
)

func SetNodeLock(nodeName string) error {
	ctx := context.Background()
	node, err := client.GetClient().CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if _, ok := node.ObjectMeta.Annotations[NodeLockTime]; ok {
		return fmt.Errorf("node %s is locked", nodeName)
	}
	newNode := node.DeepCopy()
	newNode.ObjectMeta.Annotations[NodeLockTime] = time.Now().Format(time.RFC3339)
	_, err = client.GetClient().CoreV1().Nodes().Update(ctx, newNode, metav1.UpdateOptions{})
	for i := 0; i < MaxLockRetry && err != nil; i++ {
		klog.ErrorS(err, "Failed to update node", "node", nodeName, "retry", i)
		time.Sleep(100 * time.Millisecond)
		node, err = client.GetClient().CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
		if err != nil {
			klog.ErrorS(err, "Failed to get node when retry to update", "node", nodeName)
			continue
		}
		newNode := node.DeepCopy()
		newNode.ObjectMeta.Annotations[NodeLockTime] = time.Now().Format(time.RFC3339)
		_, err = client.GetClient().CoreV1().Nodes().Update(ctx, newNode, metav1.UpdateOptions{})
	}
	if err != nil {
		return fmt.Errorf("setNodeLock exceeds retry count %d", MaxLockRetry)
	}
	klog.InfoS("Node lock set", "node", nodeName)
	return nil
}

func ReleaseNodeLock(nodeName string) error {
	ctx := context.Background()
	node, err := client.GetClient().CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if _, ok := node.ObjectMeta.Annotations[NodeLockTime]; !ok {
		klog.InfoS("Node lock not set", "node", nodeName)
		return nil
	}
	newNode := node.DeepCopy()
	delete(newNode.ObjectMeta.Annotations, NodeLockTime)
	_, err = client.GetClient().CoreV1().Nodes().Update(ctx, newNode, metav1.UpdateOptions{})
	for i := 0; i < MaxLockRetry && err != nil; i++ {
		klog.ErrorS(err, "Failed to update node", "node", nodeName, "retry", i)
		time.Sleep(100 * time.Millisecond)
		node, err = client.GetClient().CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
		if err != nil {
			klog.ErrorS(err, "Failed to get node when retry to update", "node", nodeName)
			continue
		}
		newNode := node.DeepCopy()
		delete(newNode.ObjectMeta.Annotations, NodeLockTime)
		_, err = client.GetClient().CoreV1().Nodes().Update(ctx, newNode, metav1.UpdateOptions{})
	}
	if err != nil {
		return fmt.Errorf("releaseNodeLock exceeds retry count %d", MaxLockRetry)
	}
	klog.InfoS("Node lock released", "node", nodeName)
	return nil
}

func LockNode(nodeName string) error {
	ctx := context.Background()
	node, err := client.GetClient().CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if _, ok := node.ObjectMeta.Annotations[NodeLockTime]; !ok {
		return SetNodeLock(nodeName)
	}
	lockTime, err := time.Parse(time.RFC3339, node.ObjectMeta.Annotations[NodeLockTime])
	if err != nil {
		return err
	}
	if time.Since(lockTime) > time.Minute*5 {
		klog.InfoS("Node lock expired", "node", nodeName, "lockTime", lockTime)
		err = ReleaseNodeLock(nodeName)
		if err != nil {
			klog.ErrorS(err, "Failed to release node lock", "node", nodeName)
			return err
		}
		return SetNodeLock(nodeName)
	}
	return fmt.Errorf("node %s has been locked within 5 minutes", nodeName)
}
