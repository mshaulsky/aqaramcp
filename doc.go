// Package aqaramcp reads an Aqara Home account's devices through the Aqara
// MCP server, the cloud service at https://agent.aqara.com.
//
// # Why this exists
//
// Aqara's Open API needs a developer account that is reviewed by hand, and a
// personal application can sit in that queue indefinitely. The MCP server
// needs nothing of the sort: signing in at https://agent.aqara.com/login with
// an ordinary Aqara account hands out an API key, and the key alone reads
// every device in the account. The service is built for language models —
// its tool descriptions are Chinese prose — but its answers are structured
// tables, which is all a program needs.
//
// # Protocol
//
// The server speaks MCP over streamable HTTP: JSON-RPC 2.0 requests in POST
// bodies, answered as plain JSON or as a server-sent event stream. The client
// opens the session on first use, keeps the session ID the server assigns,
// and reopens the session transparently when the server forgets it. A
// [Client] is safe for concurrent use; calls on one session are serialised.
//
// # Signing in
//
// Keys expire after some days and there is no refresh token; the login page
// simply signs in again, with one plain request. [WithLogin] gives the client
// the account's credentials — the MD5 of the password, which is all the
// service ever receives, never the password itself — so it fetches its first
// key by itself and replaces a rejected one, repeating the call that met the
// rejection. [Login] alone performs the
// sign-in for other uses of the key.
//
// # What comes back
//
// Every tool answers with a table: the first row names the columns, the rest
// are devices. [Client.Devices] and [Client.Statuses] turn the two tables a
// dashboard needs into [Device] and [DeviceStatus] values; [Client.Call] runs
// any other tool and hands back the raw answer.
//
// # Example
//
//	client, err := aqaramcp.New(apiKey)
//	if err != nil {
//		return err
//	}
//	statuses, err := client.Statuses(ctx, aqaramcp.StatusFilter{})
//	if err != nil {
//		return err
//	}
//	for _, s := range statuses {
//		log.Printf("%s: online=%t %v", s.DeviceName, s.Status.Online(), s.Status)
//	}
package aqaramcp
