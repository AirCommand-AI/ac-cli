import { test, expect, mock } from "bun:test";
import { createServer, type Socket } from "node:net";
import { mkdtempSync, mkdirSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

const waitFor=async(predicate:()=>boolean)=>{for(let i=0;i<100;i++){if(predicate())return;await Bun.sleep(5)}throw new Error("session event timed out")};

test("switching to a new conversation detaches old agent before pending subscription",async()=>{
 const originalHome=process.env.HOME;const home=mkdtempSync(join(tmpdir(),"ac-pi-session-"));process.env.HOME=home;
 const socketPath=join(home,".aircommand","daemon","daemon.sock");mkdirSync(join(home,".aircommand","daemon"),{recursive:true});
 const operations:{op:string;sessionId?:string}[]=[];let subscriptions=0;
 const sockets:Socket[]=[];
 const server=createServer(socket=>{sockets.push(socket);let buffer="";socket.on("data",bytes=>{buffer+=bytes.toString();let end=buffer.indexOf("\n");while(end>=0){const req=JSON.parse(buffer.slice(0,end)) as {op:string;sessionId?:string};buffer=buffer.slice(end+1);operations.push(req);
  socket.write('{"ok":true,"data":{}}\n');
  if(req.op==="session.subscribe"&&++subscriptions===1)socket.write('{"type":"connect","agentId":"agm_test","workstream":"478","offset":0}\n');
  end=buffer.indexOf("\n");
 }})});
 try{await new Promise<void>(resolve=>server.listen(socketPath,resolve));
  mock.module("typebox",()=>({Type:{Object:(x:unknown)=>x,String:()=>({})}}));
  mock.module("node:os",()=>({homedir:()=>home,tmpdir}));
  const {default:extension}=await import("./index.ts");
  const handlers=new Map<string,(_event:unknown,ctx:any)=>Promise<void>>();let sessionId="first";
  const pi={registerFlag(){},getFlag(){return undefined},registerTool(){},registerCommand(){},on(name:string,handler:any){handlers.set(name,handler)},sendMessage(){},sendUserMessage(){}} as any;
  extension(pi);
  const ctx={sessionManager:{getSessionId:()=>sessionId},cwd:home,hasUI:false,model:undefined,thinkingLevel:"",abort(){}};
  await handlers.get("session_start")!({},ctx);
  await waitFor(()=>operations.some(o=>o.op==="session.attach"&&o.sessionId==="first"));
  sessionId="new-conversation";
  await handlers.get("session_start")!({},ctx);
  await waitFor(()=>subscriptions>=2);
  const detach=operations.findIndex(o=>o.op==="session.detach");
  const lookup=operations.findIndex((o,i)=>i>detach&&o.op==="session.lookup"&&o.sessionId==="new-conversation");
  expect(detach).toBeGreaterThan(-1);expect(lookup).toBeGreaterThan(detach);
  expect(operations.filter(o=>o.op==="session.attach"&&o.sessionId==="new-conversation")).toHaveLength(0);
  await handlers.get("session_shutdown")!({},ctx);
 }finally{sockets.forEach(s=>s.destroy());server.close();rmSync(home,{recursive:true,force:true});process.env.HOME=originalHome}
});
