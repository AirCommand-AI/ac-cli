import { test, expect } from "bun:test";
import { createServer } from "node:net";
import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { daemonCall, subscribeDaemon, retryDelay } from "./daemon";

test("daemon call and session subscription decode frames", async () => {
 const dir=mkdtempSync(join(tmpdir(),"ac-session-"));const path=join(dir,"daemon.sock");
 const server=createServer(socket=>{let buffer="";socket.on("data",bytes=>{buffer+=bytes.toString();if(!buffer.includes("\n"))return;
  const req=JSON.parse(buffer.slice(0,buffer.indexOf("\n"))) as {op:string};
  if(req.op==="session.lookup")socket.end('{"ok":true,"data":{"agentId":"agm_test","workstream":"478"}}\n');
  else socket.end('{"ok":true}\n{"type":"future-event","other":"ignored"}\n{"type":"connect","agentId":"agm_test","offset":8}\n{"type":"detached","reason":"stopped"}\n');
 })});
 try{await new Promise<void>(resolve=>server.listen(path,resolve));
  expect(await daemonCall(path,{op:"session.lookup",sessionId:"conv"})).toEqual({agentId:"agm_test",workstream:"478"});
  const messages:string[]=[];
  await new Promise<void>((resolve,reject)=>{const stop=subscribeDaemon(path,42,m=>{messages.push(m.type);if(m.type==="detached"){stop();resolve()}},reject)});
  expect(messages).toEqual(["connect","detached"]);
  expect([retryDelay(0),retryDelay(1),retryDelay(8)]).toEqual([1000,2000,30000]);
 }finally{server.close();rmSync(dir,{recursive:true,force:true})}
});
