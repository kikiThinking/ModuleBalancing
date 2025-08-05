## Modulebalancingserver

### Feature:
> 1. Client module file download
> 2. Client expiration module file management
> 3. Local module file expiration management


### GRPC API:

#### Module
```
  service Module {
      rpc Download(ModuleDownloadRequest) returns (stream ModuleDownloadResponse);
      rpc Analyzing(analyzingRequest) returns (analyzingResponse);
      rpc Upload (stream UploadRequest) returns (UploadResponse) {}
  }
  
  # for download api
  message ModuleDownloadRequest {
      string serveraddress = 1;
      string filename = 2;
      int64 offset = 3;
  }

  message ModuleDownloadResponse {
      bytes  content = 4;
      bool   completed = 3;
  }
  
  # for analyzing api
  message analyzingRequest {
      string  filename = 1;
      bytes   fbytes = 2;
  }

  message analyzingResponse {
      repeated string modulename = 1;
      repeated string analyzingerror = 2;
  }
  
  message UploadRequest {
      oneof data {
          finformation information = 1;
          bytes chunk_data = 2;
      }
  }

  message finformation {
      string filename = 1;
      string size = 2;
      string crc64 = 3;
      string MUnix = 4;
      string CUnix = 5;
  }

  message UploadResponse {
      string message = 1;
      bool success = 2;
  }
```
* Download
    > for client download module file api


* Analyzing
  > for client requests to parse AOD files api

* Upload
  > for backup module file to backup server