/* Native HTSlib oracle only: source offsets and opaque encoded block bytes.
 * Never linked into a candidate representation decoder. */
#include <stdint.h>
#include <htslib/cram.h>
#include <htslib/hfile.h>
typedef void (*block_cb)(int64_t,int64_t,int32_t,const void*,int,int,int);
int scan_cram(const char *path,block_cb cb){
 cram_fd *fd=cram_open(path,"rb");if(!fd)return 1;
 int blocks=0;cram_container *c;
 while((c=cram_read_container(fd))){
  int n=cram_container_get_num_blocks(c);
  if(n<0||n>1000000-blocks){cram_free_container(c);cram_close(fd);return 2;}
  for(int i=0;i<n;i++){
   int64_t start=htell(cram_fd_get_fp(fd));cram_block *b=cram_read_block(fd);
   if(!b){cram_free_container(c);cram_close(fd);return 3;}
   int32_t size=cram_block_get_comp_size(b);
   if(size<0||size>16*1024*1024){cram_free_block(b);cram_free_container(c);cram_close(fd);return 4;}
   cb(start,htell(cram_fd_get_fp(fd)),size,cram_block_get_data(b),cram_block_get_method(b),cram_block_get_content_type(b),cram_block_get_content_id(b));
   cram_free_block(b);blocks++;
  }
  cram_free_container(c);
 }
 return cram_close(fd);
}
