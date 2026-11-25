package oakestra

import (
	"encoding/json"
	"fmt"
)

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
	// The system manager reads organization_name, not organization, and falls back to the root
	// organization when it is absent.
	OrganizationName string `json:"organization_name,omitempty"`
}

type loginResponse struct {
	Token        string `json:"token"`
	RefreshToken string `json:"refresh_token"`
}

// sla is the deployment descriptor accepted by POST /api/application/ and POST /api/service/.
type sla struct {
	SLAVersion   string           `json:"sla_version"`
	CustomerID   string           `json:"customerID"`
	Applications []slaApplication `json:"applications"`
}

type slaApplication struct {
	ApplicationID        string `json:"applicationID,omitempty"`
	ApplicationName      string `json:"application_name"`
	ApplicationNamespace string `json:"application_namespace"`
	ApplicationDesc      string `json:"application_desc,omitempty"`
	// Microservices must be an empty list rather than null when creating an application;
	// register_app calls len() on it.
	Microservices []slaMicroservice `json:"microservices"`
}

type slaMicroservice struct {
	Name           string          `json:"microservice_name"`
	Namespace      string          `json:"microservice_namespace"`
	Virtualization string          `json:"virtualization"`
	Code           string          `json:"code"`
	Cmd            []string        `json:"cmd,omitempty"`
	Environment    []string        `json:"environment,omitempty"`
	OneShot        bool            `json:"one_shot,omitempty"`
	VCPUs          int             `json:"vcpus,omitempty"`
	Memory         int             `json:"memory,omitempty"`
	Constraints    []slaConstraint `json:"constraints,omitempty"`
}

type slaConstraint struct {
	Type    string `json:"type"`
	Cluster string `json:"cluster,omitempty"`
	Node    string `json:"node,omitempty"`
}

type applicationInfo struct {
	ApplicationID        string `json:"applicationID"`
	ApplicationName      string `json:"application_name"`
	ApplicationNamespace string `json:"application_namespace"`
}

type createServiceResult struct {
	JobID          flexString      `json:"job_id"`
	FailedServices []serviceResult `json:"failed_services"`
}

type serviceResult struct {
	ServiceName string `json:"service_name"`
	Message     string `json:"message"`
	Status      int    `json:"status"`
}

type serviceDoc struct {
	ID               flexString `json:"_id"`
	MicroserviceName string     `json:"microservice_name"`
	InstanceList     []instance `json:"instance_list"`
}

type instance struct {
	InstanceNumber flexInt    `json:"instance_number"`
	ClusterID      flexString `json:"cluster_id"`
	Status         string     `json:"status"`
	WorkerID       flexString `json:"worker_id"`
}

type clusterDoc struct {
	ID            flexString                 `json:"_id"`
	ClusterName   string                     `json:"cluster_name"`
	CandidateName string                     `json:"candidate_name"`
	Location      string                     `json:"cluster_location"`
	CandLocation  string                     `json:"candidate_location"`
	ActiveNodes   float64                    `json:"active_nodes"`
	VCPUs         float64                    `json:"vcpus"`
	Memory        float64                    `json:"memory"`
	PerArch       map[string]json.RawMessage `json:"aggregation_per_architecture"`
}

// flexString decodes ids that Mongo serialization may emit as a string, a number or extended
// JSON ({"$oid": "..."}).
type flexString string

func (s *flexString) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		return nil
	}
	var str string
	if json.Unmarshal(b, &str) == nil {
		*s = flexString(str)
		return nil
	}
	var o struct {
		OID string `json:"$oid"`
	}
	if json.Unmarshal(b, &o) == nil && o.OID != "" {
		*s = flexString(o.OID)
		return nil
	}
	var n json.Number
	if err := json.Unmarshal(b, &n); err != nil {
		return fmt.Errorf("oakestra: id: %w", err)
	}
	*s = flexString(n.String())
	return nil
}

// flexInt decodes a number the server may have serialized as a string.
type flexInt int

func (n *flexInt) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		return nil
	}
	var num json.Number
	if json.Unmarshal(b, &num) != nil {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return fmt.Errorf("oakestra: number: %w", err)
		}
		num = json.Number(s)
	}
	f, err := num.Float64()
	if err != nil {
		return nil
	}
	*n = flexInt(f)
	return nil
}
