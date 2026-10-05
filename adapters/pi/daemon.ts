import { createConnection, type Socket } from "node:net";
import { readFileSync } from "node:fs";
import { execFileSync } from "node:child_process";

// Match the daemon's takeover process identity on Linux and macOS.
export function processStart(pid:number):string {
 if(process.platform==="linux"){
  const stat=readFileSync(`/proc/${pid}/stat`,"utf8");const end=stat.lastIndexOf(")");
  if(end<0)throw new Error("Invalid process identity");
  const fields=stat.slice(end+1).trim().split(/\s+/);
  if(fields.length<20)throw new Error("Invalid process identity");
  return fields[19];
 }
 return execFileSync("ps",["-p",String(pid),"-o","lstart="],{encoding:"utf8"}).trim();
}

export interface SessionMessage {
 type: "connect" | "wake" | "nudge" | "interrupt" | "detached";
 agentId?: string;
 workstream?: string;
 offset?: number;
 line?: string;
 text?: string;
 reason?: string;
}

// The daemon owns reconnection to AirCommand; the add-on only reconnects to
// its user-only local socket. Keep a new connection for each control request.
export function daemonCall<T = unknown>(path: string, request: Record<string, unknown>): Promise<T> {
 return new Promise((resolve,reject) => {
  const socket=createConnection(path);let buffer="";let settled=false;
  const fail=(error: Error)=>{if(settled)return;settled=true;socket.destroy();reject(error)};
  socket.setTimeout(10000,()=>fail(new Error("AirCommand daemon did not respond")));
  socket.on("error",fail);
  socket.on("connect",()=>socket.write(JSON.stringify(request)+"\n"));
  socket.on("data",chunk=>{buffer+=chunk.toString("utf8");const end=buffer.indexOf("\n");if(end<0)return;
   try{const response=JSON.parse(buffer.slice(0,end)) as {ok:boolean;data?:T;error?:{code:string;message:string}};
    if(!response.ok)throw new Error(response.error?.message||"AirCommand daemon rejected request");
    settled=true;socket.destroy();resolve(response.data as T);
   }catch(error){fail(error instanceof Error?error:new Error("Invalid AirCommand daemon response"))}
  });
  socket.on("close",()=>{if(!settled)fail(new Error("AirCommand daemon disconnected"))});
 });
}

export function subscribeDaemon(path:string,pid:number,onMessage:(message:SessionMessage)=>void,onError:(error:Error)=>void):()=>void {
 const socket:Socket=createConnection(path);let buffer="";let ready=false;let closed=false;
 const fail=(error:Error)=>{if(closed)return;closed=true;socket.destroy();onError(error)};
 socket.on("connect",()=>socket.write(JSON.stringify({op:"session.subscribe",sessionPid:pid})+"\n"));
 socket.on("data",chunk=>{buffer+=chunk.toString("utf8");if(buffer.length>1024*1024){fail(new Error("AirCommand daemon sent an oversized frame"));return}
  let end=buffer.indexOf("\n");while(end>=0){const line=buffer.slice(0,end);buffer=buffer.slice(end+1);
   try{const value=JSON.parse(line) as Record<string,unknown>;
    if(!ready){if(value.ok!==true)throw new Error((value.error as {message?:string}|undefined)?.message||"AirCommand daemon refused subscription");ready=true}
    else if(typeof value.type==="string" && ["connect","wake","nudge","interrupt","detached"].includes(value.type)){onMessage(value as unknown as SessionMessage)}
    else if(typeof value.type!=="string")throw new Error("Invalid AirCommand daemon session frame");
    // Ignore future daemon event kinds rather than disconnecting the stream.
   }catch(error){fail(error instanceof Error?error:new Error("Invalid AirCommand daemon frame"));return}
   end=buffer.indexOf("\n");
  }
 });
 socket.on("error",fail);
 socket.on("close",()=>{if(!closed)fail(new Error("AirCommand daemon stream closed"))});
 return ()=>{closed=true;socket.destroy()};
}

export function retryDelay(attempt:number):number {return Math.min(30_000,1000*Math.pow(2,Math.min(attempt,5)))}
