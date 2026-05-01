# tsdb-influxdb

# TO see all metrics in influxdb
from(bucket: "gpu_metrics_raw")
  |> range(start: 0) // Look from the beginning of time (1970) to now
  |> group()        // Merge all separate tables into one big table

# total raws
from(bucket: "gpu_metrics_raw")
  |> range(start: 0)
  |> group()
  |> count()

# list gpu_id
from(bucket: "gpu_metrics_raw")
            |> range(start: 0)
            |> filter(fn: (r) => r._measurement == "gpu_metrics")
            |> filter(fn: (r) => r.gpu_id != "unknown")
            |> group() 
            |> distinct(column: "gpu_id")
            |> keep(columns: ["_value"])

# list gpu_id
from(bucket: "gpu_metrics_raw")
    |> range(start: 0)
    |> filter(fn: (r) => r._measurement == "gpu_metrics")
    |> filter(fn: (r) => r.gpu_id != "unknown")
    |> keep(columns: ["gpu_id"])
    |> group()
    |> distinct(column: "gpu_id")

# get all data for gpu_id no start and end time
from(bucket: "gpu_metrics_raw")
        |> range(start: 0, stop: now())
        |> filter(fn: (r) => r._measurement == "gpu_metrics")
        |> filter(fn: (r) => r.gpu_id == "0")
        |> pivot(rowKey:["_time"], columnKey: ["_field"], valueColumn: "_value")
        |> sort(columns: ["_time"], desc: false)

# get all data for gpu_id with start and end date
from(bucket: "gpu_metrics_raw")
    |> range(start: 2026-04-22T00:00:00Z, stop: 2026-04-26T00:00:00Z)
    |> filter(fn: (r) => r._measurement == "gpu_metrics")
    |> filter(fn: (r) => r.gpu_id == "0")
    |> pivot(rowKey:["_time"], columnKey: ["_field"], valueColumn: "_value")
    |> sort(columns: ["_time"], desc: false)