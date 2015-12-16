/***** BEGIN LICENSE BLOCK *****
# This Source Code Form is subject to the terms of the Mozilla Public
# License, v. 2.0. If a copy of the MPL was not distributed with this file,
# You can obtain one at http://mozilla.org/MPL/2.0/.
#
# The Initial Developer of the Original Code is the Mozilla Foundation.
# Portions created by the Initial Developer are Copyright (C) 2012
# the Initial Developer. All Rights Reserved.
#
# Contributor(s):
#   Rob Miller (rmiller@mozilla.com)
#   Mike Trinkala (trink@mozilla.com)
#
# ***** END LICENSE BLOCK *****/

package plugins

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Jeffail/gabs"
	"io/ioutil"
	"net"
	"net/http"
	"os"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	countFile = "/tmp/logspout/logspout.json"
)

var (
	UUID        string
	UserId      string
	ClusterId   string
	Hostname    string
	IP          string
	M1          map[string]string
	counter     map[string]int64
	counterlock sync.Mutex
)

type Message struct {
	FrameWorks []struct {
		Executors []struct {
			Container string `json:"container"`
			Tasks     []struct {
				SlaveId   string `json:"slave_id"`
				State     string `json:"state"`
				Name      string `json:"name"`
				Id        string `json:"id"`
				Resources struct {
					Ports string `json:"ports"`
				} `json:"resources"`
			} `json:"tasks"`
		} `json:"executors"`
	} `json:"frameworks"`
	HostName string `json:"hostname"`
}

func CheckWritePermission(fp string) (err error) {
	var file *os.File
	if file, err = ioutil.TempFile(fp, ".hekad.perm_check"); err == nil {
		errMsgs := make([]string, 0, 3)
		var e error
		if _, e = file.WriteString("ok"); e != nil {
			errMsgs = append(errMsgs, "can't write to test file")
		}
		if e = file.Close(); e != nil {
			errMsgs = append(errMsgs, "can't close test file")
		}
		if e = os.Remove(file.Name()); e != nil {
			errMsgs = append(errMsgs, "can't remove test file")
		}
		if len(errMsgs) > 0 {
			err = fmt.Errorf("errors: %s", strings.Join(errMsgs, ", "))
		}
	}
	return
}

func init() {
	loadCounter()
	UUID = os.Getenv("HOST_ID")
	if UUID == "" {
		fmt.Println("cat't found uuid")
		os.Exit(0)
	}
	UserId = os.Getenv("USER_ID")
	if UserId == "" {
		fmt.Println("cat't found userid")
		os.Exit(0)
	}
	ClusterId = os.Getenv("CLUSTER_ID")
	if ClusterId == "" {
		fmt.Println("cat't found clusterid")
		os.Exit(0)
	}
	Hostname, _ = os.Hostname()
	IP, _ = GetIp()
	go utilrun()
}

func GetIp() (ip string, err error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return "", err
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 {
			continue // interface down
		}
		if iface.Flags&net.FlagLoopback != 0 {
			continue // loopback interface
		}
		addrs, err := iface.Addrs()
		if err != nil {
			return "", err
		}
		for _, addr := range addrs {
			var ip net.IP
			switch v := addr.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if ip == nil || ip.IsLoopback() {
				continue
			}
			ip = ip.To4()
			if ip == nil {
				continue // not an ipv4 address
			}
			return ip.String(), nil
		}
	}
	return "", errors.New("are you connected to the network?")
}

func utilrun() {
	mesoslock := &sync.Mutex{}
	timer := time.NewTicker(5 * time.Second)
	for {
		select {
		case <-timer.C:
			getMesosInfo(mesoslock)
			persistenCounter()
		}
	}
}

func getMesosInfo(lock *sync.Mutex) {
	data, err := HttpGet("http://" + IP + ":5051/slave(1)/state.json")
	if err == nil {
		mg := getCnames()
		var m Message
		json.Unmarshal([]byte(data), &m)
		if len(m.FrameWorks) > 0 {
			for _, fw := range m.FrameWorks {
				if len(fw.Executors) > 0 {
					for _, ex := range fw.Executors {
						if len(ex.Tasks) > 0 {
							for _, ts := range ex.Tasks {
								mcn := "/mesos-" + ts.SlaveId + "." + ex.Container
								mg[mcn] = ts.Name + " " +
									ts.Id + " " +
									getPort(ts.Resources.Ports)
							}
						}
					}
				}
			}
		}
		lock.Lock()
		M1 = mg
		lock.Unlock()
	}
}

func getPort(ports string) string {
	reg := regexp.MustCompile("\\[|\\]")
	strs := strings.Split(reg.ReplaceAllString(ports, ""), "-")
	startPort, serr := strconv.Atoi(strs[0])
	endPort, eerr := strconv.Atoi(strs[1])
	if serr != nil || eerr != nil {
		return "[]"
	}
	var port []string
	for i := startPort; i <= endPort; i++ {
		port = append(port, strconv.Itoa(i))
	}
	return "[" + strings.Join(port, ",") + "]"

}

func getCnames() map[string]string {
	cnames := os.Getenv("CNAMES")
	if cnames != "" {
		cmap := make(map[string]string)
		ca := strings.Split(cnames, ",")
		for _, cname := range ca {
			rname := strings.Replace(cname, "/", "", 1)
			cmap[cname] = rname + " " + rname + " " + "[]"
		}
		return cmap
	}
	return make(map[string]string)
}

func HttpGet(url string) (string, error) {
	resp, err := http.Get(url)
	if err != nil {
		// handle error
		return "", err
	}

	defer resp.Body.Close()
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		// handle error
		return "", err
	}
	return string(body), err
}

func SendMessage(cn, msg, cid string) string {
	counter[cid]++
	t := time.Now()
	timestr := t.Format("2006-01-02T15:04:05.000Z")
	logmsg := timestr + " " +
		UserId + " " +
		fmt.Sprint(counter[cid]) + " " +
		ClusterId + " " +
		UUID + " " +
		IP + " " +
		Hostname + " " +
		cn + " " +
		msg
	return logmsg
}

func loadCounter() {
	counter = make(map[string]int64)
	buf, err := ioutil.ReadFile(countFile)
	if err == nil {
		json, err := gabs.ParseJSON(buf)
		if err == nil {
			m, err := json.ChildrenMap()
			if err == nil {
				for k, v := range m {
					if reflect.TypeOf(v.Data()).String() == "float64" {
						counter[k] = int64(v.Data().(float64))
					}
				}
				fmt.Println("load container counter: ", json)
			} else {
				fmt.Println("counter childrenmap error: ", err)
			}
		} else {
			fmt.Println("counter str to json error: ", err)
		}
	} else {
		fmt.Println("not found counter file: ", err)
	}
}

func persistenCounter() {
	if len(counter) > 0 {
		json := gabs.New()
		counterlock.Lock()
		for k, v := range counter {
			json.Set(v, k)
		}
		counterlock.Unlock()
		err := ioutil.WriteFile(countFile, json.Bytes(), 0x644)
		if err != nil {
			fmt.Println("persisten counter failed: ", err)
		}
	}
}
