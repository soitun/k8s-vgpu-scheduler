/*
 * Copyright © 2021 peizhaoyou <peizhaoyou@4paradigm.com>
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package scheduler

import (
	"sort"
	"strings"

	"4pd.io/k8s-vgpu/pkg/device"
	"4pd.io/k8s-vgpu/pkg/util"
	v1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"
)

type NodeScore struct {
	nodeID  string
	devices util.PodDevices
	score   float32
}

type NodeScoreList []*NodeScore

func (l DeviceUsageList) Len() int {
	return len(l)
}

func (l DeviceUsageList) Swap(i, j int) {
	l[i], l[j] = l[j], l[i]
}

func (l DeviceUsageList) Less(i, j int) bool {
	if l[i].Numa == l[j].Numa {
		return l[i].Count-l[i].Used < l[j].Count-l[j].Used
	}
	return l[i].Numa < l[j].Numa
}

func (l NodeScoreList) Len() int {
	return len(l)
}

func (l NodeScoreList) Swap(i, j int) {
	l[i], l[j] = l[j], l[i]
}

func (l NodeScoreList) Less(i, j int) bool {
	return l[i].score < l[j].score
}

func viewStatus(usage NodeUsage) {
	klog.InfoS("devices status")
	for _, val := range usage.Devices {
		klog.InfoS("device status", "device id", val.Id, "device detail", val)
	}
}

func checkType(annos map[string]string, d util.DeviceUsage, n util.ContainerDeviceRequest) (bool, bool) {
	//General type check, NVIDIA->NVIDIA MLU->MLU
	if !strings.Contains(d.Type, n.Type) {
		return false, false
	}
	for _, val := range device.GetDevices() {
		found, pass, numaAssert := val.CheckType(annos, d, n)
		if found {
			return pass, numaAssert
		}
	}
	klog.Infof("Unrecognized device", n.Type)
	return false, false
}

func fitInCertainDevice(node *NodeUsage, request util.ContainerDeviceRequest, annos map[string]string, pod *v1.Pod) (bool, []util.ContainerDevice) {
	k := request
	originReq := k.Nums
	prevnuma := -1
	klog.InfoS("Allocating device for container request", "pod", klog.KObj(pod), "card request", k)
	var tmpDevs []util.ContainerDevice
	for i := len(node.Devices) - 1; i >= 0; i-- {
		klog.InfoS("scoring pod", "pod", klog.KObj(pod), "Memreq", k.Memreq, "MemPercentagereq", k.MemPercentagereq, "Coresreq", k.Coresreq, "Nums", k.Nums, "device index", i, "device", node.Devices[i].Id)
		found, numa := checkType(annos, *node.Devices[i], k)
		if !found {
			klog.InfoS("card type mismatch,continueing...", "pod", klog.KObj(pod), node.Devices[i].Type, k.Type)
			continue
		}
		if numa && prevnuma != node.Devices[i].Numa {
			klog.InfoS("Numa not fit, resotoreing", "pod", klog.KObj(pod), "k.nums", k.Nums, "numa", numa, "prevnuma", prevnuma, "device numa", node.Devices[i].Numa)
			k.Nums = originReq
			prevnuma = node.Devices[i].Numa
			tmpDevs = []util.ContainerDevice{}
		}

		memreq := int32(0)
		if node.Devices[i].Count <= node.Devices[i].Used {
			continue
		}
		if k.Coresreq > 100 {
			klog.ErrorS(nil, "core limit can't exceed 100", "pod", klog.KObj(pod))
			return false, tmpDevs
		}
		if k.Memreq > 0 {
			memreq = k.Memreq
		}
		if k.MemPercentagereq != 101 && k.Memreq == 0 {
			//This incurs an issue
			memreq = node.Devices[i].Totalmem * k.MemPercentagereq / 100
		}
		if node.Devices[i].Totalmem-node.Devices[i].Usedmem < memreq {
			klog.V(5).InfoS("card Insufficient remaining memory", "pod", klog.KObj(pod), "device index", i, "device", node.Devices[i].Id, "device total memory", node.Devices[i].Totalmem, "device used memory", node.Devices[i].Usedmem, "request memory", memreq)
			continue
		}
		if node.Devices[i].Totalcore-node.Devices[i].Usedcores < k.Coresreq {
			klog.V(5).InfoS("card Insufficient remaining cores", "pod", klog.KObj(pod), "device index", i, "device", node.Devices[i].Id, "device total core", node.Devices[i].Totalcore, "device used core", node.Devices[i].Usedcores, "request cores", k.Coresreq)
			continue
		}
		// Coresreq=100 indicates it want this card exclusively
		if node.Devices[i].Totalcore == 100 && k.Coresreq == 100 && node.Devices[i].Used > 0 {
			klog.V(5).InfoS("the container wants exclusive access to an entire card, but the card is already in use", "pod", klog.KObj(pod), "device index", i, "device", node.Devices[i].Id, "used", node.Devices[i].Used)
			continue
		}
		// You can't allocate core=0 job to an already full GPU
		if node.Devices[i].Totalcore != 0 && node.Devices[i].Usedcores == node.Devices[i].Totalcore && k.Coresreq == 0 {
			klog.V(5).InfoS("can't allocate core=0 job to an already full GPU", "pod", klog.KObj(pod), "device index", i, "device", node.Devices[i].Id)
			continue
		}
		if k.Nums > 0 {
			klog.InfoS("first fitted", "pod", klog.KObj(pod), "device", node.Devices[i].Id)
			k.Nums--
			tmpDevs = append(tmpDevs, util.ContainerDevice{
				Idx:       i,
				UUID:      node.Devices[i].Id,
				Type:      k.Type,
				Usedmem:   memreq,
				Usedcores: k.Coresreq,
			})
		}
		if k.Nums == 0 {
			klog.InfoS("device allocate success", "pod", klog.KObj(pod), "allocate device", tmpDevs)
			return true, tmpDevs
		}
	}
	return false, tmpDevs
}

func fitInDevices(node *NodeUsage, requests []util.ContainerDeviceRequest, annos map[string]string, pod *v1.Pod) (bool, float32, []util.ContainerDevice) {
	devs := []util.ContainerDevice{}
	total := int32(0)
	free := int32(0)
	sums := 0
	//This loop is for requests for different devices
	for _, k := range requests {
		sums += int(k.Nums)
		if int(k.Nums) > len(node.Devices) {
			klog.InfoS("request devices nums cannot exceed the total number of devices on the node.", "pod", klog.KObj(pod), "request devices nums", k.Nums, "node device nums", len(node.Devices))
			return false, 0, devs
		}
		sort.Sort(node.Devices)
		fit, tmpDevs := fitInCertainDevice(node, k, annos, pod)
		if fit {
			for _, val := range tmpDevs {
				total += node.Devices[val.Idx].Count
				free += node.Devices[val.Idx].Count - node.Devices[val.Idx].Used
				node.Devices[val.Idx].Used++
				node.Devices[val.Idx].Usedcores += val.Usedcores
				node.Devices[val.Idx].Usedmem += val.Usedmem
			}
			devs = append(devs, tmpDevs...)
		} else {
			return false, 0, devs
		}
	}
	return true, float32(total)/float32(free) + float32(len(node.Devices)-sums), devs
}

func calcScore(nodes *map[string]*NodeUsage, errMap *map[string]string, nums [][]util.ContainerDeviceRequest, annos map[string]string, task *v1.Pod) (*NodeScoreList, error) {
	res := make(NodeScoreList, 0, len(*nodes))
	for nodeID, node := range *nodes {
		viewStatus(*node)
		score := NodeScore{nodeID: nodeID, score: 0}

		//This loop is for different container request
		for _, n := range nums {
			sums := 0
			for _, k := range n {
				sums += int(k.Nums)
			}
			if sums == 0 {
				score.devices = append(score.devices, util.ContainerDevices{})
				continue
			}
			klog.V(5).InfoS("fitInDevices", "pod", klog.KObj(task), "node", nodeID)
			fit, nodescore, devs := fitInDevices(node, n, annos, task)
			if fit {
				score.devices = append(score.devices, devs)
				klog.InfoS("calcScore:pod fit node score results", "pod", klog.KObj(task), "node", nodeID, "score", nodescore)
				score.score += nodescore
			} else {
				klog.InfoS("calcScore:node not fit pod", "pod", klog.KObj(task), "node", nodeID)
				break
			}
		}
		if len(score.devices) == len(nums) {
			res = append(res, &score)
		}
	}
	return &res, nil
}
