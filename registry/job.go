package registry

import (
	"errors"
	"path"
	"strings"

	etcdErr "github.com/coreos/fleet/third_party/github.com/coreos/etcd/error"
	"github.com/coreos/fleet/third_party/github.com/coreos/go-etcd/etcd"
	log "github.com/coreos/fleet/third_party/github.com/golang/glog"

	"github.com/coreos/fleet/event"
	"github.com/coreos/fleet/job"
)

const (
	jobPrefix     = "/job/"
	payloadPrefix = "/payload/"
)

func (r *Registry) GetAllPayloads() []job.JobPayload {
	var payloads []job.JobPayload

	key := path.Join(keyPrefix, payloadPrefix)
	resp, err := r.etcd.Get(key, true, true)

	if err != nil {
		return payloads
	}

	for _, node := range resp.Node.Nodes {
		var jp job.JobPayload
		//TODO: Handle the error generated by unmarshal
		unmarshal(node.Value, &jp)

		if err != nil {
			log.Errorf(err.Error())
			continue
		}

		payloads = append(payloads, jp)
	}

	return payloads
}

// List the jobs all Machines are scheduled to run
func (r *Registry) GetAllJobs() []job.Job {
	var jobs []job.Job

	key := path.Join(keyPrefix, jobPrefix)
	resp, err := r.etcd.Get(key, true, true)

	if err != nil {
		return jobs
	}

	for _, node := range resp.Node.Nodes {
		if j := r.GetJob(path.Base(node.Key)); j != nil {
			jobs = append(jobs, *j)
		}
	}

	return jobs
}

func (r *Registry) GetAllJobsByMachine(machBootID string) []job.Job {
	var jobs []job.Job

	key := path.Join(keyPrefix, jobPrefix)
	resp, err := r.etcd.Get(key, true, true)

	if err != nil {
		log.Errorf(err.Error())
		return jobs
	}

	for _, node := range resp.Node.Nodes {
		if j := r.GetJob(path.Base(node.Key)); j != nil {
			tgt := r.GetJobTarget(j.Name)
			if tgt != "" && tgt == machBootID {
				jobs = append(jobs, *j)
			}
		}
	}

	return jobs
}

// GetJobTarget looks up where the given job is scheduled. If the job has
// been scheduled, the boot ID the target machine is returned. Otherwise,
// an empty string is returned.
func (r *Registry) GetJobTarget(jobName string) string {
	// Figure out to which Machine this Job is scheduled
	key := jobTargetAgentPath(jobName)
	resp, err := r.etcd.Get(key, false, true)
	if err != nil {
		return ""
	}

	return resp.Node.Value
}

func (r *Registry) GetJob(jobName string) *job.Job {
	key := path.Join(keyPrefix, jobPrefix, jobName, "object")
	resp, err := r.etcd.Get(key, false, true)

	// Assume the error was KeyNotFound and return an empty data structure
	if err != nil {
		return nil
	}

	var jm jobModel
	//TODO: Handle the error generated by unmarshal
	unmarshal(resp.Node.Value, &jm)

	if jm.Payload == nil {
		return nil
	}

	return job.NewJob(jm.Name, *(jm.Payload))
}

type jobModel struct {
	Name    string
	Payload *job.JobPayload
}

func (r *Registry) CreatePayload(jp *job.JobPayload) error {
	key := path.Join(keyPrefix, payloadPrefix, jp.Name)
	json, _ := marshal(jp)
	_, err := r.etcd.Create(key, json, 0)
	return err
}

func (r *Registry) GetPayload(payloadName string) *job.JobPayload {
	key := path.Join(keyPrefix, payloadPrefix, payloadName)
	resp, err := r.etcd.Get(key, false, true)

	// Assume the error was KeyNotFound and return an empty data structure
	if err != nil {
		return nil
	}

	var jp job.JobPayload
	//TODO: Handle the error generated by unmarshal
	unmarshal(resp.Node.Value, &jp)

	return &jp
}

func (r *Registry) DestroyPayload(payloadName string) {
	key := path.Join(keyPrefix, payloadPrefix, payloadName)
	r.etcd.Delete(key, false)
}

func (r *Registry) CreateJob(j *job.Job) (err error) {
	key := path.Join(keyPrefix, jobPrefix, j.Name, "object")
	json, _ := marshal(j)

	if r.JobScheduled(j.Name) {
		_, err = r.etcd.Update(key, json, 0)
	} else {
		_, err = r.etcd.Create(key, json, 0)
		if err != nil && err.(*etcd.EtcdError).ErrorCode == etcdErr.EcodeNodeExist {
			err = errors.New("job already exists")
		}
	}
	return
}

func (r *Registry) ScheduleJob(jobName string, machBootID string) error {
	key := jobTargetAgentPath(jobName)
	_, err := r.etcd.Create(key, machBootID, 0)
	return err
}

func (r *Registry) UnscheduleJob(jobName string) {
	key := jobTargetAgentPath(jobName)
	r.etcd.Delete(key, true)
}

func (r *Registry) StopJob(jobName string) {
	key := path.Join(keyPrefix, jobPrefix, jobName)
	r.etcd.Delete(key, true)
}

func (r *Registry) LockJob(jobName, context string) *TimedResourceMutex {
	return r.lockResource("job", jobName, context)
}

func (r *Registry) JobScheduled(jobName string) bool {
	key := jobTargetAgentPath(jobName)
	value, err := r.etcd.Get(key, false, true)
	return err == nil && value != nil
}

func filterEventJobCreated(resp *etcd.Response) *event.Event {
	if resp.Action != "create" {
		return nil
	}

	baseName := path.Base(resp.Node.Key)
	if baseName != "object" {
		return nil
	}

	var j job.Job
	err := unmarshal(resp.Node.Value, &j)
	if err != nil {
		log.V(1).Infof("Failed to deserialize Job: %s", err)
		return nil
	}

	return &event.Event{"EventJobCreated", j, nil}
}

func filterEventJobScheduled(resp *etcd.Response) *event.Event {
	if resp.Action != "create" {
		return nil
	}

	dir, baseName := path.Split(resp.Node.Key)
	if baseName != "target" {
		return nil
	}

	jobName := path.Base(strings.TrimSuffix(dir, "/"))

	return &event.Event{"EventJobScheduled", jobName, resp.Node.Value}
}

func filterEventJobStopped(resp *etcd.Response) *event.Event {
	if resp.Action != "delete" && resp.Action != "expire" {
		return nil
	}

	dir, jobName := path.Split(resp.Node.Key)
	dir = strings.TrimSuffix(dir, "/")
	dir, prefixName := path.Split(dir)

	if prefixName != "job" {
		return nil
	}

	return &event.Event{"EventJobStopped", jobName, nil}
}

func jobTargetAgentPath(jobName string) string {
	return path.Join(keyPrefix, jobPrefix, jobName, "target")
}
