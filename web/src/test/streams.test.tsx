import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { expect, it, vi } from "vitest";
import StreamPreferences from "../pages/StreamPreferences";

it("opens an empty installation with nullable language lists and saves preferences", async () => {
 let saved: Record<string, unknown> | undefined;
 vi.stubGlobal("fetch",vi.fn(async(input:RequestInfo|URL,init?:RequestInit)=>{
  const url=String(input);let data:unknown=[];
  if(url.endsWith("/users"))data={users:[]};
  else if(url.endsWith("/stream-preferences/0")){data={audioLanguages:null,subtitleLanguages:null,subtitleMode:"off",subtitleStyle:"normal"};if(init?.method==="PUT"){saved=JSON.parse(String(init.body));data=saved}}
  return new Response(JSON.stringify(data),{status:200});
 }));
 render(<StreamPreferences/>);
 const audio=screen.getByLabelText("Audio languages, in preference order");
 await waitFor(()=>expect(audio).toBeEnabled());expect(audio).toHaveValue("");
 fireEvent.change(audio,{target:{value:"en, fr"}});fireEvent.click(screen.getByRole("button",{name:"Save preferences"}));
 await waitFor(()=>expect(saved?.audioLanguages).toEqual(["en","fr"]));
 expect(await screen.findByRole("status")).toHaveTextContent("Stream preferences saved");
});
