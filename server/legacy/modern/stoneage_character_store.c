#include "stoneage_character_store.h"
#include <errno.h>
#include <fcntl.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/stat.h>
#include <unistd.h>

int StoneAge_CharacterStoreWrite(const char *path,const char *data)
{
    char *temporary=NULL,*parent=NULL,*slash;
    size_t length,used=0;
    ssize_t count;
    int file=-1,directory=-1,ok=0,created=0,saved_errno;
    if(!path || !*path || !data){errno=EINVAL;return -1;}
    temporary=malloc(strlen(path)+16);parent=strdup(path);
    if(!temporary || !parent){errno=ENOMEM;goto done;}
    sprintf(temporary,"%s.tmp.XXXXXX",path);
    slash=strrchr(parent,'/');
    if(slash){if(slash==parent)slash[1]=0;else *slash=0;}
    else strcpy(parent,".");
    directory=open(parent,O_RDONLY|O_DIRECTORY);
    if(directory<0)goto done;
    file=mkstemp(temporary);
    if(file<0)goto done;
    created=1;
    length=strlen(data);
    while(used<length) {
        count=write(file,data+used,length-used);
        if(count<0 && errno==EINTR)continue;
        if(count<=0){if(!count)errno=EIO;goto done;}
        used+=(size_t)count;
    }
    if(fsync(file)<0)goto done;
    if(close(file)<0){file=-1;goto done;}file=-1;
    if(rename(temporary,path)<0)goto done;
    if(fsync(directory)<0)goto done;
    ok=1;
done:
    saved_errno=errno;
    if(file>=0)close(file);
    if(!ok && created)unlink(temporary);
    if(directory>=0)close(directory);
    free(temporary);free(parent);errno=saved_errno;
    return ok?0:-1;
}
