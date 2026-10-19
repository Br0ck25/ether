const n=e=>`${e.title||e.username}${e.isOwner?" (owner)":""}`;function r(e){return`?days=${e.days}&user=${encodeURIComponent(e.user)}${e.year?`&year=${e.year}`:""}`}export{r as a,n as v};
