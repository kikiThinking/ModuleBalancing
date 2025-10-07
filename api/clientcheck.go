package api

import (
	"ModuleBalancing/clientcontrol"
	rpc "ModuleBalancing/grpc"
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type ClientCheck struct {
	rpc.UnimplementedClientCheckServer
	ClientUpdateControl *clientcontrol.ClientControl
}

func (the *ClientCheck) MD5(_ context.Context, _ *rpc.MD5Request) (*rpc.MD5Response, error) {
	return &rpc.MD5Response{MD5: the.ClientUpdateControl.MD5()}, nil
}

func (the *ClientCheck) Data(request *rpc.DataRequest, stream rpc.ClientCheck_DataServer) error {
	var buffer = make([]byte, 1*1024*1024) // 1MB
	var content *bufio.Reader
	if request.Offset > 0 {
		content = bufio.NewReader(bytes.NewReader(the.ClientUpdateControl.Data()[:request.Offset]))
	} else {
		content = bufio.NewReader(bytes.NewReader(the.ClientUpdateControl.Data()))
	}

	if err = stream.SendHeader(metadata.New(map[string]string{
		"MD5":      the.ClientUpdateControl.MD5(),
		"Filename": "ModuleBalancingClient.exe",
		"Size":     fmt.Sprintf("%d", len(the.ClientUpdateControl.Data())),
	})); err != nil {
		return err
	}

	for {
		number, err := content.Read(buffer)
		if err != nil {
			if err == io.EOF {
				break // 文件读取完毕
			}
			return status.Errorf(codes.Internal, "failed to read file: %v", err)
		}

		if err = stream.Send(&rpc.DataResponse{
			Content:   buffer[:number],
			Completed: false,
		}); err != nil {
			return status.Errorf(codes.Internal, "failed to send chunk: %v", err)
		}
	}

	return stream.Send(&rpc.DataResponse{
		Content:   nil,
		Completed: true,
	})
}
