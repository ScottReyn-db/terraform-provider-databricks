package sharing

import (
	"context"
	"sort"

	"reflect"

	"github.com/databricks/databricks-sdk-go/service/sharing"
	"github.com/databricks/terraform-provider-databricks/common"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

type SharesAPI struct {
	client  *common.DatabricksClient
	context context.Context
}

func NewSharesAPI(ctx context.Context, m any) SharesAPI {
	return SharesAPI{m.(*common.DatabricksClient), context.WithValue(ctx, common.Api, common.API_2_1)}
}

const (
	ShareAdd    = "ADD"
	ShareRemove = "REMOVE"
	ShareUpdate = "UPDATE"
)

type ShareInfo struct {
	Name      string             `json:"name" tf:"force_new"`
	Comment   string             `json:"comment,omitempty"`
	Owner     string             `json:"owner,omitempty" tf:"suppress_diff"`
	Objects   []SharedDataObject `json:"objects,omitempty" tf:"slice_set,alias:object"`
	CreatedAt int64              `json:"created_at,omitempty" tf:"computed"`
	CreatedBy string             `json:"created_by,omitempty" tf:"computed"`
}

type SharedDataObject struct {
	Name                     string      `json:"name"`
	DataObjectType           string      `json:"data_object_type"`
	Comment                  string      `json:"comment,omitempty"`
	SharedAs                 string      `json:"shared_as,omitempty" tf:"suppress_diff"`
	CDFEnabled               bool        `json:"cdf_enabled,omitempty" tf:"suppress_diff"`
	StartVersion             int64       `json:"start_version,omitempty" tf:"suppress_diff"`
	HistoryDataSharingStatus string      `json:"history_data_sharing_status,omitempty" tf:"suppress_diff"`
	Partitions               []Partition `json:"partitions,omitempty" tf:"alias:partition"`
	Status                   string      `json:"status,omitempty" tf:"computed"`
	AddedAt                  int64       `json:"added_at,omitempty" tf:"computed"`
	AddedBy                  string      `json:"added_by,omitempty" tf:"computed"`
}

type ShareDataChange struct {
	Action     string           `json:"action"`
	DataObject SharedDataObject `json:"data_object"`
}

type ShareUpdates struct {
	Owner   string            `json:"owner,omitempty"`
	Updates []ShareDataChange `json:"updates"`
}

func (su *ShareUpdates) sortSharesByName() {
	sort.Slice(su.Updates, func(i, j int) bool {
		return su.Updates[i].DataObject.Name < su.Updates[j].DataObject.Name
	})
}

type Shares struct {
	Shares []ShareInfo `json:"shares"`
}

type Partition struct {
	Values []PartitionValue `json:"values" tf:"alias:value"`
}

type PartitionValue struct {
	Name                 string `json:"name"`
	Op                   string `json:"op"`
	RecipientPropertyKey string `json:"recipient_property_key,omitempty"`
	Value                string `json:"value,omitempty"`
}

func (si *ShareInfo) sortSharesByName() {
	sort.Slice(si.Objects, func(i, j int) bool {
		return si.Objects[i].Name < si.Objects[j].Name
	})
}

func (si *ShareInfo) suppressCDFEnabledDiff() {
	//suppress diff for CDF Enabled if HistoryDataSharingStatus is enabled , as API does not accept both fields to be set
	for i := range si.Objects {
		if si.Objects[i].HistoryDataSharingStatus == "ENABLED" {
			si.Objects[i].CDFEnabled = false
		}
	}
}

func (a SharesAPI) get(name string) (si ShareInfo, err error) {
	err = a.client.Get(a.context, "/unity-catalog/shares/"+name+"?include_shared_data=true", nil, &si)
	si.sortSharesByName()
	si.suppressCDFEnabledDiff()
	return
}

func (a SharesAPI) update(name string, su ShareUpdates) error {
	if len(su.Updates) == 0 {
		return nil
	}
	su.sortSharesByName()
	return a.client.Patch(a.context, "/unity-catalog/shares/"+name, su)
}

func (si ShareInfo) shareChanges(action string) ShareUpdates {
	var changes []ShareDataChange
	for _, obj := range si.Objects {
		changes = append(changes, ShareDataChange{
			Action:     action,
			DataObject: obj,
		})
	}
	return ShareUpdates{
		Updates: changes,
	}
}

func (si ShareInfo) resourceShareMap() map[string]SharedDataObject {
	return resourceShareMapForShares(si.Objects)
}

func resourceShareMapForShares(objects []SharedDataObject) map[string]SharedDataObject {
	m := make(map[string]SharedDataObject, len(objects))
	for _, sdo := range objects {
		m[sdo.Name] = sdo
	}
	return m
}

func (sdo SharedDataObject) Equal(other SharedDataObject) bool {
	if other.SharedAs == "" {
		other.SharedAs = sdo.SharedAs
	}
	//don't compare computed fields
	other.AddedAt = sdo.AddedAt
	other.AddedBy = sdo.AddedBy
	other.Status = sdo.Status
	return reflect.DeepEqual(sdo, other)
}

func Diff(beforeMap map[string]SharedDataObject, afterMap map[string]SharedDataObject) []ShareDataChange {
	changes := []ShareDataChange{}
	// not in after so remove
	for _, beforeSdo := range beforeMap {
		_, exists := afterMap[beforeSdo.Name]
		if exists {
			continue
		}
		changes = append(changes, ShareDataChange{
			Action:     ShareRemove,
			DataObject: beforeSdo,
		})
	}

	// not in before so add
	// if in before but diff then update
	for _, afterSdo := range afterMap {
		if afterSdo.Name == "" {
			continue
		}
		beforeSdo, exists := beforeMap[afterSdo.Name]
		if exists {
			if !beforeSdo.Equal(afterSdo) {
				// do not send SharedAs
				afterSdo.SharedAs = ""
				changes = append(changes, ShareDataChange{
					Action:     ShareUpdate,
					DataObject: afterSdo,
				})
			}
			continue
		}
		changes = append(changes, ShareDataChange{
			Action:     ShareAdd,
			DataObject: afterSdo,
		})
	}
	return changes
}

func (beforeSi ShareInfo) Diff(afterSi ShareInfo) []ShareDataChange {
	beforeMap := beforeSi.resourceShareMap()
	afterMap := afterSi.resourceShareMap()
	return Diff(beforeMap, afterMap)
}

func ResourceShare() common.Resource {
	shareSchema := common.StructToSchema(ShareInfo{}, func(m map[string]*schema.Schema) map[string]*schema.Schema {
		m["name"].DiffSuppressFunc = common.EqualFoldDiffSuppress
		m["object"].Set = nil
		return m
	})
	return common.Resource{
		Schema: shareSchema,
		Create: func(ctx context.Context, d *schema.ResourceData, c *common.DatabricksClient) error {
			w, err := c.WorkspaceClient()
			if err != nil {
				return err
			}

			var createRequest sharing.CreateShare
			common.DataToStructPointer(d, shareSchema, &createRequest)
			if _, err := w.Shares.Create(ctx, createRequest); err != nil {
				return err
			}

			//can only create empty share, objects & owners have to be added using update API
			var si ShareInfo
			common.DataToStructPointer(d, shareSchema, &si)
			shareChanges := si.shareChanges(ShareAdd)
			shareChanges.Owner = si.Owner
			if err := NewSharesAPI(ctx, c).update(si.Name, shareChanges); err != nil {
				//delete orphaned share if update fails
				if d_err := w.Shares.DeleteByName(ctx, si.Name); d_err != nil {
					return d_err
				}
				return err
			}
			d.SetId(si.Name)
			return nil
		},
		Read: func(ctx context.Context, d *schema.ResourceData, c *common.DatabricksClient) error {
			si, err := NewSharesAPI(ctx, c).get(d.Id())
			if err != nil {
				return err
			}
			return common.StructToData(si, shareSchema, d)
		},
		Update: func(ctx context.Context, d *schema.ResourceData, c *common.DatabricksClient) error {
			beforeSi, err := NewSharesAPI(ctx, c).get(d.Id())
			if err != nil {
				return err
			}

			var afterSi ShareInfo
			common.DataToStructPointer(d, shareSchema, &afterSi)
			// changes := beforeSi.Diff(afterSi)

			w, err := c.WorkspaceClient()
			if err != nil {
				return err
			}

			// @TODO: do the rollback
			if d.HasChanges("owner", "comment") {
				_, err = w.Shares.Update(ctx, sharing.UpdateShare{
					Name:    d.Get("name").(string),
					Owner:   d.Get("owner").(string),
					Comment: d.Get("comment").(string),
				})
			}

			if err != nil {
				return err
			}

			if !d.HasChange("object") {
				return nil
			}

			oldObjects, newObjects := d.GetChange("object")
			tfOldObjects := oldObjects.(*schema.Set)
			tfNewObjects := newObjects.(*schema.Set)

			objectsToRemove := tfOldObjects.Difference(tfNewObjects).List()
			objectsToAdd := tfNewObjects.Difference(tfOldObjects).List()

			apiChanges := []ShareDataChange{}

			for _, raw := range objectsToRemove {
				rawMap := raw.(map[string]interface{})
				removal := SharedDataObject{
					Name:           rawMap["name"].(string),
					Comment:        rawMap["comment"].(string),
					DataObjectType: rawMap["data_object_type"].(string),
				}

				apiChanges = append(apiChanges, ShareDataChange{
					Action:     ShareRemove,
					DataObject: removal,
				})
			}

			terraformShares := d.Get("object").(*schema.Set).List()
			for _, existingShare := range beforeSi.Objects {
				keepShare := false

				for _, rawTfShare := range terraformShares {
					rawMap := rawTfShare.(map[string]interface{})
					tfObject := SharedDataObject{
						Name:           rawMap["name"].(string),
						Comment:        rawMap["comment"].(string),
						DataObjectType: rawMap["data_object_type"].(string),
					}
					if existingShare.Equal(tfObject) {
						keepShare = true
					}
				}
				if !keepShare {
					// @TODO: do we need a clone without the computed fields?
					apiChanges = append(apiChanges, ShareDataChange{
						Action:     ShareRemove,
						DataObject: existingShare,
					})
				}
			}

			for _, raw := range objectsToAdd {
				rawMap := raw.(map[string]interface{})
				updateObject := SharedDataObject{
					Name:           rawMap["name"].(string),
					Comment:        rawMap["comment"].(string),
					DataObjectType: rawMap["data_object_type"].(string),
				}

				// Look at the api call back result and see if the share already exists
				exists := false
				for _, existingDataObject := range beforeSi.Objects {
					// This is an exact match. What should happen when the match is partial
					// updateObject doesn't have the SharedAs field, and thus Equal behaves differently
					if existingDataObject.Equal(updateObject) {
						exists = true
						break
					}
				}

				if !exists {
					apiChanges = append(apiChanges, ShareDataChange{
						Action:     ShareAdd,
						DataObject: updateObject,
					})
				}
			}

			err = NewSharesAPI(ctx, c).update(d.Id(), ShareUpdates{
				Updates: apiChanges,
			})
			if err != nil {
				// Rollback
				if d.HasChanges("owner", "comment") {
					oldOwner, _ := d.GetChange("owner")
					oldComment, _ := d.GetChange("comment")
					_, rollbackErr := w.Shares.Update(ctx, sharing.UpdateShare{
						Name:    d.Get("name").(string),
						Owner:   oldOwner.(string),
						Comment: oldComment.(string),
					})
					if rollbackErr != nil {
						// @TODO: just just owner update failed. could be the comment
						return common.OwnerRollbackError(err, rollbackErr, beforeSi.Owner, afterSi.Owner)
					}
				}

				return err
			}
			return nil
		},
		Delete: func(ctx context.Context, d *schema.ResourceData, c *common.DatabricksClient) error {
			w, err := c.WorkspaceClient()
			if err != nil {
				return err
			}
			return w.Shares.DeleteByName(ctx, d.Id())
		},
	}
}
