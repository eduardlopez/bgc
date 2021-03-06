package bgc

import (
	"errors"
	"fmt"
	"github.com/viant/dsc"
	"github.com/viant/toolbox"
	"google.golang.org/api/bigquery/v2"
)

var useLegacySQL = "/* USE LEGACY SQL */"
var queryPageSize int64 = 500
var tickInterval = 100
var doneStatus = "DONE"

//QueryIterator represetns a QueryIterator.
type QueryIterator struct {
	*queryTask
	schema         *bigquery.TableSchema
	jobCompleted   bool
	jobReferenceID string
	Rows           []*bigquery.TableRow
	rowsIndex      uint64
	resultInfo     *QueryResultInfo
	pageToken      string
	totalRows      uint64
	processedRows  uint64
}

//HasNext returns true if there is next row to fetch.
func (qi *QueryIterator) HasNext() bool {
	return qi.processedRows < qi.totalRows
}

//Unwarpping big query nested result
func unwrapValueIfNeeded(value interface{}, field *bigquery.TableFieldSchema) interface{} {
	if fieldMap, ok := value.(map[string]interface{}); ok {
		if wrappedValue, ok := fieldMap["v"]; ok {
			if field.Fields != nil {
				return unwrapValueIfNeeded(wrappedValue, field)
			}
			return wrappedValue
		}

		if fieldValue, ok := fieldMap["f"]; ok {
			if field.Fields != nil {
				var newMap = make(map[string]interface{})
				toolbox.ProcessSliceWithIndex(fieldValue, func(index int, item interface{}) bool {
					newMapValue := unwrapValueIfNeeded(item, field.Fields[index])
					newMap[field.Fields[index].Name] = newMapValue
					index++
					return true
				})
				return newMap
			}
		}

		panic("Should not be here " + fmt.Sprintf("%v", value))
	}
	if slice, ok := value.([]interface{}); ok {
		var newSlice = make([]interface{}, 0)
		for _, item := range slice {
			value := unwrapValueIfNeeded(item, field)
			newSlice = append(newSlice, value)
		}
		return newSlice
	}
	return value
}

func toValue(source interface{}, field *bigquery.TableFieldSchema) interface{} {
	switch sourceValue := source.(type) {
	case []interface{}:
		var newSlice = make([]interface{}, 0)
		for _, item := range sourceValue {
			itemValue := unwrapValueIfNeeded(item, field)
			newSlice = append(newSlice, itemValue)
		}
		return newSlice

	case map[string]interface{}:
		return unwrapValueIfNeeded(sourceValue, field)

	}

	return source

}

//Next returns next row.
func (qi *QueryIterator) Next() ([]interface{}, error) {
	qi.processedRows++
	qi.rowsIndex++
	if int(qi.rowsIndex) >= len(qi.Rows) {
		err := qi.fetchPage()
		if err != nil {
			return nil, err
		}
	}
	row := qi.Rows[qi.rowsIndex]
	fields := qi.schema.Fields
	var values = make([]interface{}, 0)
	for i, cell := range row.F {
		value := toValue(cell.V, fields[i])
		values = append(values, value)
	}
	return values, nil
}

func (qi *QueryIterator) fetchPage() error {
	queryResultCall := qi.service.Jobs.GetQueryResults(qi.projectID, qi.jobReferenceID)
	queryResultCall.MaxResults(queryPageSize).PageToken(qi.pageToken)

	jobGetResult, err := queryResultCall.Context(qi.context).Do()
	if err != nil {
		return err
	}
	if qi.totalRows == 0 {
		qi.totalRows = jobGetResult.TotalRows
	}
	if qi.resultInfo.TotalRows == 0 {
		qi.resultInfo.TotalBytesProcessed = int(jobGetResult.TotalBytesProcessed)
		qi.resultInfo.CacheHit = jobGetResult.CacheHit
		qi.resultInfo.TotalRows = int(jobGetResult.TotalRows)
	}

	qi.rowsIndex = 0
	qi.Rows = jobGetResult.Rows
	qi.pageToken = jobGetResult.PageToken
	qi.jobCompleted = jobGetResult.JobComplete
	if qi.schema == nil {
		qi.schema = jobGetResult.Schema
	}
	return nil
}

//GetColumns returns query columns, after query executed.
func (qi *QueryIterator) GetColumns() ([]string, error) {
	if qi.schema == nil {
		return nil, errors.New("Failed to get table schema")
	}
	var result = make([]string, 0)
	for _, field := range qi.schema.Fields {
		result = append(result, field.Name)
	}
	return result, nil
}

//NewQueryIterator creates a new query iterator for passed in datastore manager and query.
func NewQueryIterator(manager dsc.Manager, query string) (*QueryIterator, error) {
	service, context, err := GetServiceAndContextForManager(manager)
	if err != nil {
		return nil, err
	}

	config := manager.Config()
	var result = &QueryIterator{

		Rows: make([]*bigquery.TableRow, 0),
		queryTask: &queryTask{
			manager:   manager,
			service:   service,
			context:   context,
			projectID: config.Get(ProjectIDKey),
			datasetID: config.Get(DataSetIDKey),
		},
	}
	job, err := result.run(query)
	if err != nil {
		fmt.Printf("err:%v\n%v", err, query)
	}
	if err != nil {
		return nil, err
	}
	result.jobReferenceID = job.JobReference.JobId
	result.resultInfo = &QueryResultInfo{}
	err = result.fetchPage()

	if err != nil {
		return nil, err
	}
	return result, nil
}
